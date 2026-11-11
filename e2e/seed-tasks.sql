-- Give the e2e person a fixed set of tasks to browse. e2e/ensure-brain.sh runs
-- this against the e2e compose project only, right after e2e/reset.sql, so
-- every run starts from the same list. It is safe to run again: it first
-- deletes every task the e2e person owns (the e2e database holds nothing else
-- of theirs), then inserts the set below.
--
-- Rows go in the way the brain writes them, so every constraint and trigger in
-- internal/tasks/migrations still applies: each task opens with an `open`
-- event, every status change follows an event whose edge matches, a task is
-- Done only with a passing check batch, a result belongs to a subtask, and a
-- subtask draws its budget from its parent.
--
--   ...0001  Sort the morning mail            Working (Working), one chat line
--   ...0002  Plan the launch email            Working (Planning)
--   ...0003  Email the supplier statements    Needs approval, one pending approval
--   ...0004  Close the week for Kodeeo        Failed, with a cause
--   ...0005  Close the books for October      Working, parent of 0006 and 0007
--   ...0006  Reconcile the accounts           Done, with a {summary, detail} result
--   ...0007  Draft the October summary        Working
--   ...0008  Draft the renewal notice         Needs approval, one pending approval
--
-- The e2e person's own provider accounts are cleared as well (not seeded), so
-- the providers check always starts from an empty list.
--
-- Each waiting task also gets its pending approval and the person their
-- notifications, so the approval card and the bell have live rows to show. Both
-- go in the way the brain writes them: an approval is pending and owned by the
-- one who decides, and a notification is one of proposal, question, finished or
-- failed, about a task or about nothing.
--
-- The person's chats are seeded below too, so the chat page reads live rows:
--   ...c101  What should the October close check first?  user line, assistant line at $0.0031
--   ...c102  Summarise the supplier prices              assistant line, cost unknown
--   ...c103  New chat                                   empty
--
-- A user line carries no model and no cost, an assistant line names its model,
-- and each line keeps its own time so the order stays stable.

SELECT id::text AS ada FROM identity.users WHERE lower(email) = 'ada@example.com' \gset

-- Delete first. The event tables refuse deletes, so this transaction turns
-- triggers off (superuser only). Nothing is inserted here.
BEGIN;
SET LOCAL session_replication_role = replica;
CREATE TEMP TABLE mine ON COMMIT DROP AS
  SELECT id FROM tasks.tasks WHERE owner_user_id = :'ada'::uuid;
DELETE FROM tasks.automation_fires WHERE task_id IN (SELECT id FROM mine);
DELETE FROM tasks.run_events WHERE run_id IN (SELECT id FROM tasks.runs WHERE task_id IN (SELECT id FROM mine));
DELETE FROM tasks.runs WHERE task_id IN (SELECT id FROM mine);
DELETE FROM tasks.checks WHERE task_id IN (SELECT id FROM mine);
DELETE FROM tasks.chat_events WHERE task_id IN (SELECT id FROM mine);
DELETE FROM tasks.task_events WHERE task_id IN (SELECT id FROM mine);
DELETE FROM tasks.approvals WHERE task_id IN (SELECT id FROM mine);
-- The shares of the seeded tasks live in identity, not under the task rows,
-- so they go too: the e2e flow shares one of these tasks and puts it back.
DELETE FROM identity.shares
WHERE object_type = 'chat' AND object_id::text LIKE 'e2e00000-0000-7000-8000-%';
DELETE FROM identity.shares
WHERE object_type = 'task'
  AND (object_id IN (SELECT id FROM mine) OR object_id::text LIKE 'e2e00000-0000-7000-8000-%');
DELETE FROM tasks.tasks WHERE id IN (SELECT id FROM mine);
-- The chats seeded below are append-only through chat.keep_line, so their lines
-- go in the same replica-mode transaction.
DELETE FROM chat.lines WHERE chat_id IN (SELECT id FROM chat.chats WHERE owner_user_id = :'ada'::uuid);
DELETE FROM chat.chats WHERE owner_user_id = :'ada'::uuid;
COMMIT;

-- The e2e person's own provider accounts go too, with their models and sealed
-- keys, so e2e/providers.mjs starts from an empty list on every run. The
-- gateway tables are under forced row security, which a superuser bypasses.
-- The keys go first: they are found through the accounts' credential refs.
BEGIN;
DELETE FROM gateway.credentials
WHERE ref IN (SELECT credential_ref FROM gateway.provider_accounts WHERE owner_user_id = :'ada'::uuid);
DELETE FROM gateway.provider_models
WHERE account_id IN (SELECT id FROM gateway.provider_accounts WHERE owner_user_id = :'ada'::uuid);
DELETE FROM gateway.provider_accounts WHERE owner_user_id = :'ada'::uuid;
COMMIT;

-- The routes and the workspace account the chat composer flow (e2e/composer.mjs)
-- sends its replies through. The account is global and a house one, the only
-- kind a route's links fall back on for a person: a pin to a person's own
-- account is paid by them alone, so with no links of theirs it could not fail
-- over. Nothing the person owns is linked, so the wipe above never meets a link.
-- The models are the ids the fake adapter scripts, and plain-1 and plain-2,
-- which answer with the adapter's default. They are replaced on every run.
--
--   e2e-main   plain-1, then fake-ok
--   e2e-slow   fake-slow
--   e2e-spare  plain-2
--
-- The models page (e2e/models.mjs) reads its routes from here too: chat, with
-- three hops, and agent-dispatch. Their links sit on a global account for an
-- adapter the gateway does not have, so a reply on the default route "chat"
-- still fails at once, as it does with no such route, and e2e/chat.mjs does
-- not see a reply text.
BEGIN;
DELETE FROM gateway.route_links
WHERE route_id IN (SELECT id FROM gateway.routes WHERE name LIKE 'e2e-%' OR name IN ('chat', 'agent-dispatch'));
DELETE FROM gateway.routes WHERE name LIKE 'e2e-%' OR name IN ('chat', 'agent-dispatch');
DELETE FROM gateway.provider_models
WHERE account_id IN (SELECT id FROM gateway.provider_accounts WHERE owner_user_id IS NULL AND name IN ('e2e-house', 'e2e-sample'));
DELETE FROM gateway.provider_accounts WHERE owner_user_id IS NULL AND name IN ('e2e-house', 'e2e-sample');

INSERT INTO gateway.provider_accounts (id, name, adapter, credential_ref, plan_kind, house, provider, status) VALUES
  ('e2e00000-0000-7000-8000-00000000f001', 'e2e-house', 'fake', 'house', 'local', true, 'e2e', 'Connected');
INSERT INTO gateway.provider_accounts (id, name, adapter, credential_ref, plan_kind, house, provider, status) VALUES
  ('e2e00000-0000-7000-8000-00000000f002', 'e2e-sample', 'anthropic', 'sample', 'metered', false, 'anthropic', 'Connected');
INSERT INTO gateway.provider_models (account_id, model, position) VALUES
  ('e2e00000-0000-7000-8000-00000000f001', 'fake-ok', 1),
  ('e2e00000-0000-7000-8000-00000000f001', 'fake-slow', 2),
  ('e2e00000-0000-7000-8000-00000000f001', 'fake-fail-ratelimit', 3),
  ('e2e00000-0000-7000-8000-00000000f001', 'fake-fail-after-2', 4),
  ('e2e00000-0000-7000-8000-00000000f001', 'plain-1', 5),
  ('e2e00000-0000-7000-8000-00000000f001', 'plain-2', 6),
  ('e2e00000-0000-7000-8000-00000000f002', 'claude-3-7-sonnet', 1),
  ('e2e00000-0000-7000-8000-00000000f002', 'gpt-4.1', 2),
  ('e2e00000-0000-7000-8000-00000000f002', 'llama3.3:70b', 3),
  ('e2e00000-0000-7000-8000-00000000f002', 'claude-3-5-haiku', 4),
  ('e2e00000-0000-7000-8000-00000000f002', 'gpt-4.1-mini', 5);
INSERT INTO gateway.routes (id, name, capability, decision) VALUES
  ('e2e00000-0000-7000-8000-00000000f101', 'e2e-main', 'chat', ''),
  ('e2e00000-0000-7000-8000-00000000f102', 'e2e-spare', 'chat', ''),
  ('e2e00000-0000-7000-8000-00000000f103', 'e2e-slow', 'chat', ''),
  ('e2e00000-0000-7000-8000-00000000f104', 'chat', 'chat', ''),
  ('e2e00000-0000-7000-8000-00000000f105', 'agent-dispatch', 'decide', 'choice');
INSERT INTO gateway.route_links (route_id, position, provider_account_id, model) VALUES
  ('e2e00000-0000-7000-8000-00000000f101', 1, 'e2e00000-0000-7000-8000-00000000f001', 'plain-1'),
  ('e2e00000-0000-7000-8000-00000000f101', 2, 'e2e00000-0000-7000-8000-00000000f001', 'fake-ok'),
  ('e2e00000-0000-7000-8000-00000000f102', 1, 'e2e00000-0000-7000-8000-00000000f001', 'plain-2'),
  ('e2e00000-0000-7000-8000-00000000f103', 1, 'e2e00000-0000-7000-8000-00000000f001', 'fake-slow'),
  ('e2e00000-0000-7000-8000-00000000f104', 1, 'e2e00000-0000-7000-8000-00000000f002', 'claude-3-7-sonnet'),
  ('e2e00000-0000-7000-8000-00000000f104', 2, 'e2e00000-0000-7000-8000-00000000f002', 'gpt-4.1'),
  ('e2e00000-0000-7000-8000-00000000f104', 3, 'e2e00000-0000-7000-8000-00000000f002', 'llama3.3:70b'),
  ('e2e00000-0000-7000-8000-00000000f105', 1, 'e2e00000-0000-7000-8000-00000000f002', 'claude-3-5-haiku'),
  ('e2e00000-0000-7000-8000-00000000f105', 2, 'e2e00000-0000-7000-8000-00000000f002', 'gpt-4.1-mini');
COMMIT;

-- The bell's rows are seeded below, so the old ones go first, dismissed or not.
DELETE FROM notices.notifications WHERE user_id = :'ada'::uuid;

-- Insert with every trigger on.
BEGIN;

-- One status change: the event first, then the task row that follows it.
CREATE FUNCTION pg_temp.step(p_task uuid, p_kind text, p_from text, p_to text, p_at timestamptz)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
  INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, at)
  SELECT p_task, COALESCE(MAX(event.seq), 0) + 1, p_from, p_to, p_kind, p_at
  FROM tasks.task_events AS event
  WHERE event.task_id = p_task;
  UPDATE tasks.tasks SET status = p_to, updated_at = p_at WHERE id = p_task;
END;
$$;

-- A task is born Preparing, with its open event at seq 1.
CREATE FUNCTION pg_temp.open_task(
  p_task uuid, p_person uuid, p_parent uuid, p_goal text, p_budget int, p_spent int, p_at timestamptz)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
  INSERT INTO tasks.tasks (id, owner_user_id, parent_id, goal, status, budget, spent, updated_at)
  VALUES (p_task, p_person, p_parent, p_goal, 'Preparing', p_budget, p_spent, p_at);
  INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, at)
  VALUES (p_task, 1, '', 'Preparing', 'open', p_at);
END;
$$;

-- The helper calls return rows nobody needs.
\o /dev/null

-- 0001: Working, with a chat line between its events.
SELECT pg_temp.open_task('e2e00000-0000-7000-8000-000000000001', :'ada'::uuid, NULL,
  'Sort the morning mail', 200, 45, now() - interval '65 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000001', 'plan', 'Preparing', 'Planning', now() - interval '64 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000001', 'advance', 'Planning', 'Working', now() - interval '63 minutes');
INSERT INTO tasks.chat_events (task_id, seq, body, at)
VALUES ('e2e00000-0000-7000-8000-000000000001', 1, 'Read the support mailbox first.', now() - interval '60 minutes');

-- 0002: Planning.
SELECT pg_temp.open_task('e2e00000-0000-7000-8000-000000000002', :'ada'::uuid, NULL,
  'Plan the launch email', 300, 0, now() - interval '55 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000002', 'plan', 'Preparing', 'Planning', now() - interval '54 minutes');

-- 0003: Needs approval, paused from Working.
SELECT pg_temp.open_task('e2e00000-0000-7000-8000-000000000003', :'ada'::uuid, NULL,
  'Email the supplier statements', 500, 210, now() - interval '50 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000003', 'plan', 'Preparing', 'Planning', now() - interval '49 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000003', 'advance', 'Planning', 'Working', now() - interval '48 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000003', 'pause', 'Working', 'Needs approval', now() - interval '47 minutes');

-- 0004: Failed. The only cause the schema allows is a cancelled parent, and it
-- may be set once the task is Failed.
SELECT pg_temp.open_task('e2e00000-0000-7000-8000-000000000004', :'ada'::uuid, NULL,
  'Close the week for Kodeeo Limited', 500, 85, now() - interval '45 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000004', 'advance', 'Preparing', 'Working', now() - interval '44 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000004', 'fail', 'Working', 'Failed', now() - interval '43 minutes');
UPDATE tasks.tasks SET cause = 'parent_cancelled' WHERE id = 'e2e00000-0000-7000-8000-000000000004';

-- 0005: the parent. Its children hold 300 + 200 of its 1000.
SELECT pg_temp.open_task('e2e00000-0000-7000-8000-000000000005', :'ada'::uuid, NULL,
  'Close the books for October', 1000, 0, now() - interval '40 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000005', 'plan', 'Preparing', 'Planning', now() - interval '39 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000005', 'advance', 'Planning', 'Working', now() - interval '38 minutes');

-- 0006: Done. Only a subtask has a result, and a task is Done only after a
-- passing check batch.
SELECT pg_temp.open_task('e2e00000-0000-7000-8000-000000000006', :'ada'::uuid, 'e2e00000-0000-7000-8000-000000000005',
  'Reconcile the accounts', 300, 120, now() - interval '37 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000006', 'advance', 'Preparing', 'Working', now() - interval '36 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000006', 'advance', 'Working', 'Checking', now() - interval '35 minutes');
INSERT INTO tasks.checks (task_id, batch, seq, name, code, at)
VALUES ('e2e00000-0000-7000-8000-000000000006', 1, 1, 'ledger reconciles', 0, now() - interval '34 minutes');
INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, at)
VALUES ('e2e00000-0000-7000-8000-000000000006', 4, 'Checking', 'Done', 'pass', now() - interval '33 minutes');
UPDATE tasks.tasks
SET status = 'Done',
    passes = true,
    result = '{"summary": "The accounts reconcile.", "detail": "All 31 ledger entries matched the bank statement."}',
    updated_at = now() - interval '33 minutes'
WHERE id = 'e2e00000-0000-7000-8000-000000000006';

-- 0007: Working.
SELECT pg_temp.open_task('e2e00000-0000-7000-8000-000000000007', :'ada'::uuid, 'e2e00000-0000-7000-8000-000000000005',
  'Draft the October summary', 200, 20, now() - interval '32 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000007', 'advance', 'Preparing', 'Working', now() - interval '31 minutes');

-- 0008: Needs approval, paused from Working, so a denial also has a task to resume.
SELECT pg_temp.open_task('e2e00000-0000-7000-8000-000000000008', :'ada'::uuid, NULL,
  'Draft the renewal notice', 300, 30, now() - interval '30 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000008', 'plan', 'Preparing', 'Planning', now() - interval '29 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000008', 'advance', 'Planning', 'Working', now() - interval '28 minutes');
SELECT pg_temp.step('e2e00000-0000-7000-8000-000000000008', 'pause', 'Working', 'Needs approval', now() - interval '27 minutes');

-- One pending approval for each waiting task, the way the brain's run asks:
-- owned by the one who decides, with the action the card shows. The decide_once
-- trigger only guards updates, so a pending insert needs no tricks.
INSERT INTO tasks.approvals (id, task_id, user_id, tool, reason, action, created_at) VALUES
  ('e2e00000-0000-7000-8000-00000000a003', 'e2e00000-0000-7000-8000-000000000003', :'ada'::uuid,
    'smtp', 'ask', 'Email the October supplier statements to ap@supplier.example', now() - interval '46 minutes'),
  ('e2e00000-0000-7000-8000-00000000a008', 'e2e00000-0000-7000-8000-000000000008', :'ada'::uuid,
    'smtp', 'ask', 'Send the renewal notice to billing@registrar.example', now() - interval '26 minutes');

-- The bell's notifications: finished and failed are about seeded tasks, a
-- proposal is about no task (the table's CHECK says so), and none is dismissed.
INSERT INTO notices.notifications (id, user_id, kind, title, task_id, created_at) VALUES
  ('e2e00000-0000-7000-8000-00000000b001', :'ada'::uuid, 'failed', 'The weekly close failed',
    'e2e00000-0000-7000-8000-000000000004', now() - interval '43 minutes'),
  ('e2e00000-0000-7000-8000-00000000b002', :'ada'::uuid, 'finished', 'The accounts are reconciled',
    'e2e00000-0000-7000-8000-000000000006', now() - interval '33 minutes'),
  ('e2e00000-0000-7000-8000-00000000b003', :'ada'::uuid, 'proposal', 'Archive the October statements',
    NULL, now() - interval '21 minutes'),
  ('e2e00000-0000-7000-8000-00000000b004', :'ada'::uuid, 'question', 'Renew the domain before Friday?',
    NULL, now() - interval '20 minutes');

\o
COMMIT;

-- Ada's chats. A chat opens private and empty; the titles the list shows are
-- what its first user line says, so the ids alone are fixed here.
BEGIN;
INSERT INTO chat.chats (id, owner_user_id, visibility, created_at) VALUES
  ('e2e00000-0000-7000-8000-00000000c101', :'ada'::uuid, 'private', now() - interval '12 minutes'),
  ('e2e00000-0000-7000-8000-00000000c102', :'ada'::uuid, 'private', now() - interval '18 minutes'),
  ('e2e00000-0000-7000-8000-00000000c103', :'ada'::uuid, 'private', now() - interval '2 hours');

INSERT INTO chat.lines (chat_id, seq, body, role, at, model, tokens, cost_micro_usd) VALUES
  ('e2e00000-0000-7000-8000-00000000c101', 1, 'What should the October close check first?', 'user',
    now() - interval '12 minutes', NULL, 0, NULL),
  ('e2e00000-0000-7000-8000-00000000c101', 2, 'Start with the supplier statements, then reconcile the ledger.', 'assistant',
    now() - interval '11 minutes', 'claude-opus-5-5', 412, 3100),
  ('e2e00000-0000-7000-8000-00000000c102', 1, 'Summarise the supplier prices', 'user',
    now() - interval '18 minutes', NULL, 0, NULL),
  ('e2e00000-0000-7000-8000-00000000c102', 2, 'Prices are stable to the year end; lead times are the open question.', 'assistant',
    now() - interval '17 minutes', 'llama3.3:70b', 58, NULL);
COMMIT;
