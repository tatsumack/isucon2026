"""Destructive SQL fixtures confined to an explicit disposable DB and schema."""
from pathlib import Path
import subprocess, re, hashlib, json, os, tempfile

R = Path(__file__).resolve().parents[2]
SCHEMA = 'isureal_event_fixture'
O = Path(tempfile.mkdtemp(prefix='isureal-event-sql-'))
_DSN = os.environ.get('ISUREAL_TEST_DATABASE_URL')
if not _DSN:
    raise SystemExit('ISUREAL_TEST_DATABASE_URL is required; use a disposable test database.')
_ENV = dict(os.environ, PGDATABASE=_DSN)
_PSQL = os.environ.get('PSQL', 'psql')

def _psql(sql):
    try:
        p = subprocess.run([_PSQL, '--no-psqlrc', '--no-password', '--quiet',
                            '--tuples-only', '--no-align', '--set=ON_ERROR_STOP=1'],
                           input=sql, text=True, capture_output=True, env=_ENV)
    except OSError:
        raise SystemExit('Cannot execute psql; no SQL fixture was run.')
    return p

# Never print the DSN, command environment, or raw connection error.
p = _psql('SELECT current_database();\n')
if p.returncode:
    raise SystemExit('Test database connection failed; DSN omitted.')
_DATABASE = p.stdout.strip()
if not re.fullmatch(r'isureal_event_test_[A-Za-z0-9_]+', _DATABASE):
    raise SystemExit('Refusing SQL: current_database() must start with isureal_event_test_.')
p = _psql(f'CREATE SCHEMA IF NOT EXISTS {SCHEMA};\n')
if p.returncode:
    raise SystemExit('Cannot prepare isolated fixture schema; details omitted.')


def expand(p):
    return re.sub(r'^\\ir (.+)$', lambda m: expand(p.parent / m[1].strip()),
                  p.read_text(), flags=re.M)


def run(name, sql):
    # Public is deliberately absent: unqualified DROP/DDL/reset cannot target it.
    sql = f'SET search_path TO {SCHEMA};\n' + sql
    p = _psql(sql)
    out = (p.stdout + p.stderr).replace(_DSN, '[DSN omitted]')
    (O / (name + '.log')).write_text(out)
    (O / (name + '.sql')).write_text(sql)
    if p.returncode or re.search(r'\b(ERROR|FATAL|PANIC):', out):
        raise RuntimeError(name + ' failed; inspect ' + str(O / (name + '.log')))
    print(name + ' PASS', flush=True)
    return out


def lit(s):
    return "'" + s.replace("'", "''") + "'"


def check(expr, msg):
    return ('DO $$ BEGIN IF NOT COALESCE((' + expr + "),false) THEN RAISE EXCEPTION '"
            + msg + "'; END IF; END $$;\n")


def hashes():
    files = list((R / 'sql').glob('*.sql')) + list((R / 'go').glob('*.go'))
    (O / 'source_sha256.json').write_text(json.dumps({
        str(p.relative_to(R)): hashlib.sha256(p.read_bytes()).hexdigest() for p in files
    }, indent=2))
