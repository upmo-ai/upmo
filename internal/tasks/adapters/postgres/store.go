package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Store is the task tables. One transition is one transaction.
type Store struct {
	Pool     *pgxpool.Pool
	Enqueuer jobs.Enqueuer
	// ReplaceTaskGrants runs identity's grant replacement inside a transaction
	// the store already opened. cmd wires it from taskGrants.
	ReplaceTaskGrants func(ctx context.Context, tx pgx.Tx, taskID string, grants []domain.ShareGrant, grantedBy string) error
}

// Transition locks the task, applies Step, and appends its events.
func (s Store) Transition(ctx context.Context, access app.Access, taskID string, input domain.Input) (app.Task, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var task app.Task
	var effect domain.Effect
	if taskID == "" {
		task, err = insertOpen(ctx, tx, access.UserID, access.Allow, input)
	} else {
		task, effect, err = stepLocked(ctx, tx, taskID, input)
	}
	if err != nil {
		return app.Task{}, err
	}
	if err := recordTransition(ctx, tx, taskID, task, effect); err != nil {
		return app.Task{}, err
	}
	if task.Status == domain.Done || task.Status == domain.Failed {
		if err := enqueueScout(ctx, tx, s, access.UserID, "task", task.ID); err != nil {
			return app.Task{}, err
		}
		if err := enqueueExtract(ctx, tx, s, access.UserID, task.ID, task.Goal); err != nil {
			return app.Task{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Task{}, err
	}
	return task, nil
}

// Record locks the task, appends one check batch, and records whether it passed.
func (s Store) Record(ctx context.Context, access app.Access, taskID string, checks []domain.Check) (bool, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, app.ErrGone
	}
	if err != nil {
		return false, fmt.Errorf("tasks: lock: %w", err)
	}
	if domain.Status(status) != domain.Checking {
		return false, domain.ErrInput
	}

	var batch int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(batch), 0) + 1
		FROM tasks.checks
		WHERE task_id = $1::uuid`, taskID).Scan(&batch); err != nil {
		return false, fmt.Errorf("tasks: check batch: %w", err)
	}
	for index, check := range checks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.checks (task_id, batch, seq, name, code)
			VALUES ($1::uuid, $2, $3, $4, $5)`,
			taskID, batch, index+1, check.Name, check.Code); err != nil {
			return false, fmt.Errorf("tasks: check: %w", err)
		}
	}

	passes := domain.Passes(checks)
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET passes = $2, updated_at = now()
		WHERE id = $1::uuid`, taskID, passes)
	if err != nil {
		return false, fmt.Errorf("tasks: passes: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, app.ErrGone
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return passes, nil
}

// Admit serializes admission for one person and creates or admits one run.
func (s Store) Admit(ctx context.Context, access app.Access, taskID, runID string) (app.Run, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Run{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0::bigint))`,
		access.UserID,
	); err != nil {
		return app.Run{}, fmt.Errorf("tasks: admission lock: %w", err)
	}

	var run app.Run
	var prior domain.RunState
	if runID == "" {
		run, err = insertRun(ctx, tx, access.UserID, taskID)
	} else {
		run, prior, err = admitRunLocked(ctx, tx, access.UserID, runID)
	}
	if err != nil {
		return app.Run{}, err
	}
	// A run already admitted changed nothing, so it records nothing.
	if runID == "" || prior != run.State {
		if err := audit.Record(audit.WithRunner(ctx, audit.CredentialRun), tx, audit.Event{
			Action:   audit.TaskRun,
			Outcome:  audit.Success,
			TargetID: run.ID,
			Changes: audit.Changes{
				Fields: []string{"state"},
				Values: map[string]audit.Change{"state": {From: string(prior), To: string(run.State)}},
			},
		}); err != nil {
			return app.Run{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Run{}, err
	}
	return run, nil
}

// End ends a queued or admitted run.
func (s Store) End(ctx context.Context, access app.Access, runID string) (app.Run, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Run{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	run, err := lockRun(ctx, tx, runID)
	if err != nil {
		return app.Run{}, err
	}
	next, err := domain.RunStep(run.State, domain.RunInput{Kind: domain.RunEnd})
	if err != nil {
		return app.Run{}, err
	}
	if next != run.State {
		if err := insertRunEvent(ctx, tx, run.ID, run.State, next, domain.RunEnd); err != nil {
			return app.Run{}, err
		}
		if err := updateRunState(ctx, tx, run.ID, next); err != nil {
			return app.Run{}, err
		}
		run.State = next
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Run{}, err
	}
	return run, nil
}

// Queued lists this person's queued runs oldest first.
func (s Store) Queued(ctx context.Context, access app.Access) ([]string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT run.id::text
		FROM tasks.runs AS run
		JOIN tasks.tasks AS task ON task.id = run.task_id
		WHERE task.owner_user_id = $1::uuid
		  AND run.state = 'queued'
		ORDER BY run.created_at, run.id`, access.UserID)
	if err != nil {
		return nil, fmt.Errorf("tasks: queued: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("tasks: queued: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: queued: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

func (s Store) begin(ctx context.Context, access app.Access) (pgx.Tx, error) {
	return s.beginWith(ctx, access, pgx.TxOptions{})
}

func (s Store) beginWith(ctx context.Context, access app.Access, opts pgx.TxOptions) (pgx.Tx, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("tasks: no database")
	}
	tx, err := s.Pool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', $3, true),
		       set_config('upmo.shared_view', $4, true),
		       set_config('upmo.shared_edit', $5, true)`,
		accessArgs(access)...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// accessArgs are the begin settings. An empty team or share list is "{}".
func accessArgs(access app.Access) []any {
	return []any{access.UserID, access.Allow, arrayOr(access.Teams), arrayOr(access.SharedView), arrayOr(access.SharedEdit)}
}

func arrayOr(literal string) string {
	if literal == "" {
		return "{}"
	}
	return literal
}

func insertRun(ctx context.Context, tx pgx.Tx, userID, taskID string) (app.Run, error) {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Run{}, app.ErrGone
	}
	if err != nil {
		return app.Run{}, fmt.Errorf("tasks: lock: %w", err)
	}
	if domain.Status(status) == domain.Done || domain.Status(status) == domain.Failed {
		return app.Run{}, domain.ErrTerminal
	}

	active, err := countAdmittedRuns(ctx, tx, userID)
	if err != nil {
		return app.Run{}, err
	}
	next, err := domain.RunStep("", domain.RunInput{
		Kind: domain.RunAdmit,
		Room: domain.Room(active),
	})
	if err != nil {
		return app.Run{}, err
	}

	run := app.Run{TaskID: taskID, State: next}
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.runs (task_id, state)
		VALUES ($1::uuid, $2)
		RETURNING id::text`, taskID, string(next)).Scan(&run.ID)
	if err != nil {
		return app.Run{}, fmt.Errorf("tasks: run: %w", err)
	}
	if err := insertRunEvent(ctx, tx, run.ID, "", next, domain.RunAdmit); err != nil {
		return app.Run{}, err
	}
	return run, nil
}

func admitRunLocked(ctx context.Context, tx pgx.Tx, userID, runID string) (app.Run, domain.RunState, error) {
	run, err := lockRun(ctx, tx, runID)
	if err != nil {
		return app.Run{}, "", err
	}
	if run.State == domain.RunAdmitted {
		return run, run.State, nil
	}

	active, err := countAdmittedRuns(ctx, tx, userID)
	if err != nil {
		return app.Run{}, "", err
	}
	next, err := domain.RunStep(run.State, domain.RunInput{
		Kind: domain.RunAdmit,
		Room: domain.Room(active),
	})
	if err != nil {
		return app.Run{}, "", err
	}
	if next == run.State {
		return run, run.State, nil
	}
	if err := insertRunEvent(ctx, tx, run.ID, run.State, next, domain.RunAdmit); err != nil {
		return app.Run{}, "", err
	}
	if err := updateRunState(ctx, tx, run.ID, next); err != nil {
		return app.Run{}, "", err
	}
	prior := run.State
	run.State = next
	return run, prior, nil
}

func lockRun(ctx context.Context, tx pgx.Tx, runID string) (app.Run, error) {
	var run app.Run
	var state string
	err := tx.QueryRow(ctx, `
		SELECT id::text, task_id::text, state
		FROM tasks.runs
		WHERE id = $1::uuid
		FOR UPDATE`, runID).Scan(&run.ID, &run.TaskID, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Run{}, app.ErrGone
	}
	if err != nil {
		return app.Run{}, fmt.Errorf("tasks: lock run: %w", err)
	}
	run.State = domain.RunState(state)
	return run, nil
}

func countAdmittedRuns(ctx context.Context, tx pgx.Tx, userID string) (int, error) {
	var active int
	err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM tasks.runs AS run
		JOIN tasks.tasks AS task ON task.id = run.task_id
		WHERE task.owner_user_id = $1::uuid
		  AND run.state = 'admitted'`, userID).Scan(&active)
	if err != nil {
		return 0, fmt.Errorf("tasks: count admitted: %w", err)
	}
	return active, nil
}

func insertRunEvent(
	ctx context.Context,
	tx pgx.Tx,
	runID string,
	from, to domain.RunState,
	input domain.RunKind,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tasks.run_events (run_id, seq, from_state, to_state, input)
		SELECT $1::uuid,
		       COALESCE((SELECT MAX(seq) FROM tasks.run_events WHERE run_id = $1::uuid), 0) + 1,
		       $2, $3, $4`,
		runID, string(from), string(to), string(input))
	if err != nil {
		return fmt.Errorf("tasks: run event: %w", err)
	}
	return nil
}

func updateRunState(ctx context.Context, tx pgx.Tx, runID string, state domain.RunState) error {
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.runs
		SET state = $2
		WHERE id = $1::uuid`, runID, string(state))
	if err != nil {
		return fmt.Errorf("tasks: run state: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return nil
}

func insertOpen(ctx context.Context, tx pgx.Tx, userID, allow string, input domain.Input) (app.Task, error) {
	next, effect, err := domain.Step("", input)
	if err != nil {
		return app.Task{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, allow)
		VALUES ($1::uuid, 'private', $2, $3, $4)
		RETURNING id::text`, userID, effect.Goal, string(next), allow).Scan(&id)
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: open: %w", err)
	}
	if err := insertTaskEvent(ctx, tx, id, effect); err != nil {
		return app.Task{}, err
	}
	return app.Task{ID: id, Status: next, Goal: effect.Goal}, nil
}

func stepLocked(ctx context.Context, tx pgx.Tx, taskID string, input domain.Input) (app.Task, domain.Effect, error) {
	var status, goal string
	var passes, paused bool
	err := tx.QueryRow(ctx, `
		SELECT status, goal, passes, paused
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status, &goal, &passes, &paused)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Task{}, domain.Effect{}, app.ErrGone
	}
	if err != nil {
		return app.Task{}, domain.Effect{}, fmt.Errorf("tasks: lock: %w", err)
	}
	if paused && (input.Kind == domain.Advance || input.Kind == domain.Plan) {
		return app.Task{}, domain.Effect{}, domain.ErrExhausted
	}
	if input.Kind == domain.Pass && !passes {
		return app.Task{}, domain.Effect{}, domain.ErrEvidence
	}
	var turnBody string
	if input.Kind == domain.Advance && domain.Status(status) == domain.Working {
		var ended bool
		turnBody, ended = domain.EndTurn(input.Turn)
		if !ended {
			return app.Task{}, domain.Effect{}, domain.ErrTurn
		}
		if turnBody != "" {
			_, effect, err := domain.Step(domain.Working, domain.Input{Kind: domain.Chat, Body: turnBody})
			if err != nil {
				return app.Task{}, domain.Effect{}, err
			}
			turnBody = effect.Body
		}
	}
	next, effect, err := domain.Step(domain.Status(status), input)
	if err != nil {
		return app.Task{}, domain.Effect{}, err
	}
	if effect.Kind == domain.Chat {
		chatID, err := insertChatEvent(ctx, tx, taskID, effect.Body)
		if err != nil {
			return app.Task{}, domain.Effect{}, err
		}
		return app.Task{ID: taskID, Status: next, Goal: goal, ChatID: chatID}, effect, nil
	}
	if turnBody != "" {
		if _, err := insertChatEvent(ctx, tx, taskID, turnBody); err != nil {
			return app.Task{}, domain.Effect{}, err
		}
	}
	if err := insertTaskEvent(ctx, tx, taskID, effect); err != nil {
		return app.Task{}, domain.Effect{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET status = $2, updated_at = now()
		WHERE id = $1::uuid`, taskID, string(next))
	if err != nil {
		return app.Task{}, domain.Effect{}, fmt.Errorf("tasks: status: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.Task{}, domain.Effect{}, app.ErrGone
	}
	if next == domain.Done || next == domain.Failed {
		// The task ended, so an approval it waits for can never be decided.
		// Close it here, in the same transaction as the step that ended it.
		if _, err := tx.Exec(ctx, `
			UPDATE tasks.approvals
			SET state = 'closed', decided_at = now()
			WHERE task_id = $1::uuid AND state = 'pending'`, taskID); err != nil {
			return app.Task{}, domain.Effect{}, fmt.Errorf("tasks: close approval: %w", err)
		}
	}
	return app.Task{ID: taskID, Status: next, Goal: goal}, effect, nil
}

// recordTransition seals the event of one transition: one action per
// transition the step took, with the status edge as its safe value. The
// event shares the transition's transaction, so a rolled-back step leaves no
// event behind.
func recordTransition(ctx context.Context, tx pgx.Tx, taskID string, task app.Task, effect domain.Effect) error {
	event := audit.Event{Outcome: audit.Success}
	if taskID == "" {
		event.Action = audit.TaskCreate
		event.TargetID = task.ID
		return audit.Record(ctx, tx, event)
	}
	event.TargetID = taskID
	switch effect.Kind {
	case domain.Chat:
		event.Action = audit.TaskChat
		event.TargetType = "line"
		event.TargetID = task.ChatID
	default:
		event.Action = audit.TaskUpdate
		if effect.Kind == domain.Fail || effect.Kind == domain.Pass {
			event.Action = audit.TaskStatusChange
		}
		event.Changes = audit.Changes{
			Fields: []string{"status"},
			Values: map[string]audit.Change{"status": {From: string(effect.From), To: string(effect.To)}},
		}
	}
	return audit.Record(ctx, tx, event)
}

func insertTaskEvent(ctx context.Context, tx pgx.Tx, taskID string, effect domain.Effect) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		SELECT $1::uuid,
		       COALESCE((SELECT MAX(seq) FROM tasks.task_events WHERE task_id = $1::uuid), 0) + 1,
		       $2, $3, $4`,
		taskID, string(effect.From), string(effect.To), string(effect.Kind))
	if err != nil {
		return fmt.Errorf("tasks: event: %w", err)
	}
	return nil
}

func insertChatEvent(ctx context.Context, tx pgx.Tx, taskID, body string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO tasks.chat_events (task_id, seq, body)
		SELECT $1::uuid,
		       COALESCE((SELECT MAX(seq) FROM tasks.chat_events WHERE task_id = $1::uuid), 0) + 1,
		       $2
		RETURNING id::text`,
		taskID, body).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("tasks: chat: %w", err)
	}
	return id, nil
}

// SaveTemplate creates or updates a template under row security.
func (s Store) SaveTemplate(ctx context.Context, access app.Access, id string, template domain.Template) (string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if id == "" {
		var newID string
		err = tx.QueryRow(ctx, `
			INSERT INTO tasks.templates (owner_user_id, goal, agent_id, budget)
			VALUES ($1::uuid, $2, $3::uuid, $4)
			RETURNING id`,
			access.UserID, template.Goal, template.AgentID, template.Budget).Scan(&newID)
		if err != nil {
			return "", fmt.Errorf("tasks: insert template: %w", err)
		}
		for i, title := range template.Outline {
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks.template_steps (template_id, seq, title)
				VALUES ($1::uuid, $2, $3)`,
				newID, i+1, title); err != nil {
				return "", fmt.Errorf("tasks: insert template step: %w", err)
			}
		}
		for i, target := range template.Deliveries {
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks.template_deliveries (template_id, seq, target)
				VALUES ($1::uuid, $2, $3)`,
				newID, i+1, target); err != nil {
				return "", fmt.Errorf("tasks: insert template delivery: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return newID, nil
	}

	var existingID string
	err = tx.QueryRow(ctx, `
		SELECT id
		FROM tasks.templates
		WHERE id = $1::uuid
		FOR UPDATE`, id).Scan(&existingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", app.ErrGone
	}
	if err != nil {
		return "", fmt.Errorf("tasks: lock template: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE tasks.templates
		SET goal = $2, agent_id = $3::uuid, budget = $4, updated_at = now()
		WHERE id = $1::uuid`,
		id, template.Goal, template.AgentID, template.Budget)
	if err != nil {
		return "", fmt.Errorf("tasks: update template: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", app.ErrGone
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM tasks.template_steps
		WHERE template_id = $1::uuid`, id); err != nil {
		return "", fmt.Errorf("tasks: delete template steps: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM tasks.template_deliveries
		WHERE template_id = $1::uuid`, id); err != nil {
		return "", fmt.Errorf("tasks: delete template deliveries: %w", err)
	}

	for i, title := range template.Outline {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.template_steps (template_id, seq, title)
			VALUES ($1::uuid, $2, $3)`,
			id, i+1, title); err != nil {
			return "", fmt.Errorf("tasks: insert template step: %w", err)
		}
	}
	for i, target := range template.Deliveries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.template_deliveries (template_id, seq, target)
			VALUES ($1::uuid, $2, $3)`,
			id, i+1, target); err != nil {
			return "", fmt.Errorf("tasks: insert template delivery: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// OpenTemplate fetches a template by id under row security.
func (s Store) OpenTemplate(ctx context.Context, access app.Access, id string) (domain.Template, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return domain.Template{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var goal, agentID string
	var budget int
	err = tx.QueryRow(ctx, `
		SELECT goal, agent_id::text, budget
		FROM tasks.templates
		WHERE id = $1::uuid`, id).Scan(&goal, &agentID, &budget)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Template{}, app.ErrGone
	}
	if err != nil {
		return domain.Template{}, fmt.Errorf("tasks: open template: %w", err)
	}

	stepRows, err := tx.Query(ctx, `
		SELECT title
		FROM tasks.template_steps
		WHERE template_id = $1::uuid
		ORDER BY seq ASC`, id)
	if err != nil {
		return domain.Template{}, fmt.Errorf("tasks: open template steps: %w", err)
	}
	defer stepRows.Close()

	outline := make([]string, 0)
	for stepRows.Next() {
		var title string
		if err := stepRows.Scan(&title); err != nil {
			return domain.Template{}, fmt.Errorf("tasks: scan template step: %w", err)
		}
		outline = append(outline, title)
	}
	if err := stepRows.Err(); err != nil {
		return domain.Template{}, fmt.Errorf("tasks: step rows: %w", err)
	}

	deliveryRows, err := tx.Query(ctx, `
		SELECT target
		FROM tasks.template_deliveries
		WHERE template_id = $1::uuid
		ORDER BY seq ASC`, id)
	if err != nil {
		return domain.Template{}, fmt.Errorf("tasks: open template deliveries: %w", err)
	}
	defer deliveryRows.Close()

	deliveries := make([]string, 0)
	for deliveryRows.Next() {
		var target string
		if err := deliveryRows.Scan(&target); err != nil {
			return domain.Template{}, fmt.Errorf("tasks: scan template delivery: %w", err)
		}
		deliveries = append(deliveries, target)
	}
	if err := deliveryRows.Err(); err != nil {
		return domain.Template{}, fmt.Errorf("tasks: delivery rows: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Template{}, err
	}

	return domain.Template{
		Goal:       goal,
		AgentID:    agentID,
		Outline:    outline,
		Deliveries: deliveries,
		Budget:     budget,
	}, nil
}

// RemoveTemplate deletes a template by id under row security.
func (s Store) RemoveTemplate(ctx context.Context, access app.Access, id string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		DELETE FROM tasks.templates
		WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("tasks: remove template: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}
