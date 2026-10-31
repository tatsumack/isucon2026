#!/usr/bin/env python3
"""Run only against an explicitly configured disposable PostgreSQL test DB."""
from pathlib import Path
import runpy
from harness import R, O, run, hashes, re

here = Path(__file__).resolve().parent
runpy.run_path(str(here / 'test_seed_reset.py'))
runpy.run_path(str(here / 'test_history_hub.py'))
migration = re.search(r'const notificationHistoryMigrationSQL = `([\s\S]*?)`',
                      (R / 'go/notification_migration.go').read_text())[1]
assert migration.strip() == (R / 'sql/005-event-notifications.sql').read_text().strip()
run('actual_startup_migration',
    "BEGIN ISOLATION LEVEL READ COMMITTED;\n"
    "SELECT pg_advisory_xact_lock(hashtext('notification_history_migration'));\n" +
    migration + 'COMMIT;\n')
hashes()
print('Evidence:', O)
print('SQL sequential checks passed; Go/live concurrency/SSE integration were not tested.')
