package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Find returns the stored id, goal and status of one task this person can see
// under row security. It runs one query.
func (s Store) Find(ctx context.Context, access app.Access, taskID string) (app.Task, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var task app.Task
	var status string
	err = tx.QueryRow(ctx, `
		SELECT id::text, goal, status
		FROM tasks.tasks
		WHERE id = $1::uuid`, taskID).Scan(&task.ID, &task.Goal, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Task{}, app.ErrGone
	}
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: find: %w", err)
	}
	task.Status = domain.Status(status)
	if err := tx.Commit(ctx); err != nil {
		return app.Task{}, err
	}
	return task, nil
}

// View returns one task this person can see under row security, with its
// events, subtasks and result.
func (s Store) View(ctx context.Context, access app.Access, taskID string) (app.TaskView, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.TaskView{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var view app.TaskView
	var status string
	var raw []byte
	err = tx.QueryRow(ctx, `
		SELECT id::text, goal, status, budget, spent, updated_at, cause, result
		FROM tasks.tasks
		WHERE id = $1::uuid`, taskID).Scan(
		&view.ID, &view.Title, &status, &view.LimitCents, &view.SpentCents, &view.UpdatedAt, &view.Detail, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.TaskView{}, app.ErrGone
	}
	if err != nil {
		return app.TaskView{}, fmt.Errorf("tasks: view: %w", err)
	}
	view.Result = domain.ResultText(raw)

	events, last, err := readEvents(ctx, tx, taskID)
	if err != nil {
		return app.TaskView{}, err
	}
	view.Events = events
	view.Subtasks, err = readSubtasks(ctx, tx, taskID)
	if err != nil {
		return app.TaskView{}, err
	}
	if err := fill(&view.TaskSummary, status, last); err != nil {
		return app.TaskView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return app.TaskView{}, err
	}
	return view, nil
}

// List returns the tasks this person can see under row security.
func (s Store) List(ctx context.Context, access app.Access) ([]app.TaskSummary, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT task.id::text, task.goal, task.status, task.budget, task.spent, task.updated_at,
		       COALESCE((
		         SELECT event.to_status
		         FROM tasks.task_events AS event
		         WHERE event.task_id = task.id
		           AND event.to_status IN ('Preparing', 'Planning', 'Working', 'Checking')
		         ORDER BY event.seq DESC
		         LIMIT 1
		       ), '')
		FROM tasks.tasks AS task
		ORDER BY task.updated_at DESC, task.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}
	defer rows.Close()

	out := []app.TaskSummary{}
	for rows.Next() {
		var task app.TaskSummary
		var status, last string
		if err := rows.Scan(&task.ID, &task.Title, &status, &task.LimitCents, &task.SpentCents, &task.UpdatedAt, &last); err != nil {
			return nil, fmt.Errorf("tasks: list: %w", err)
		}
		if err := fill(&task, status, domain.Status(last)); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// fill sets the status a person sees and the phase from the stored status and
// the last phase the task's events reached.
func fill(task *app.TaskSummary, status string, last domain.Status) error {
	task.State = domain.Status(status)
	shown, err := domain.Show(task.State)
	if err != nil {
		return fmt.Errorf("tasks: status %q: %w", status, err)
	}
	task.Status = shown
	task.Phase = domain.PhaseOf(task.State, last)
	task.UpdatedAt = task.UpdatedAt.UTC()
	return nil
}

// entry is one row of either event table, before the two are merged.
type entry struct {
	id     string
	at     time.Time
	input  string
	to     string
	body   string
	isChat bool
}

// readEvents returns a task's transition events and chat lines in time order,
// and the last phase its transitions reached. Seq orders rows within a table;
// it cannot order rows across the two, so a tie in time falls to the id, which
// is time ordered and was made in the order the rows were written.
func readEvents(ctx context.Context, tx pgx.Tx, taskID string) ([]app.Event, domain.Status, error) {
	var entries []entry
	rows, err := tx.Query(ctx, `
		SELECT id::text, at, input, to_status
		FROM tasks.task_events
		WHERE task_id = $1::uuid
		ORDER BY seq`, taskID)
	if err != nil {
		return nil, "", fmt.Errorf("tasks: events: %w", err)
	}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.at, &e.input, &e.to); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("tasks: events: %w", err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("tasks: events: %w", err)
	}

	rows, err = tx.Query(ctx, `
		SELECT id::text, at, body
		FROM tasks.chat_events
		WHERE task_id = $1::uuid
		ORDER BY seq`, taskID)
	if err != nil {
		return nil, "", fmt.Errorf("tasks: chat events: %w", err)
	}
	for rows.Next() {
		e := entry{isChat: true}
		if err := rows.Scan(&e.id, &e.at, &e.body); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("tasks: chat events: %w", err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("tasks: chat events: %w", err)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].at.Equal(entries[j].at) {
			return entries[i].at.Before(entries[j].at)
		}
		return entries[i].id < entries[j].id
	})

	events := make([]app.Event, 0, len(entries))
	var phase domain.Status
	for _, e := range entries {
		event := app.Event{ID: e.id, Time: e.at.UTC()}
		if e.isChat {
			event.Type = app.MessageType
			event.Message = e.body
			event.Phase = domain.PhaseOf("", phase)
		} else {
			message, ok := domain.EventMessage(domain.Kind(e.input), domain.Status(e.to))
			if !ok {
				return nil, "", fmt.Errorf("tasks: event %s: %w", e.id, domain.ErrInput)
			}
			phase = domain.PhaseOf(domain.Status(e.to), phase)
			event.Type = e.input
			event.Message = message
			event.Phase = phase
		}
		events = append(events, event)
	}
	return events, phase, nil
}

// readSubtasks returns the direct children of a task that this person can see.
func readSubtasks(ctx context.Context, tx pgx.Tx, taskID string) ([]app.SubtaskView, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, goal, status
		FROM tasks.tasks
		WHERE parent_id = $1::uuid
		ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: subtasks: %w", err)
	}
	defer rows.Close()

	out := []app.SubtaskView{}
	for rows.Next() {
		var sub app.SubtaskView
		var status string
		if err := rows.Scan(&sub.ID, &sub.Title, &status); err != nil {
			return nil, fmt.Errorf("tasks: subtasks: %w", err)
		}
		shown, err := domain.Show(domain.Status(status))
		if err != nil {
			return nil, fmt.Errorf("tasks: status %q: %w", status, err)
		}
		sub.Status = shown
		out = append(out, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: subtasks: %w", err)
	}
	return out, nil
}
