# API Documentation & Demo

## When to Use

- Setting up for a customer demo or investor pitch
- Updating API docs after a new feature or endpoint change
- Onboarding new engineers to the API surface
- Validating docs after a release (CI/CD docs-check)
- Debugging Mintlify rendering issues
- Preparing the tenant-scale demo with 20K+ tenants

## Impact

- Incorrect docs lead to failed integrations and support load.
- Stale endpoint schemas cause SDK code-generation failures.
- A broken demo during a sales call loses the deal.

**Severity:** P3 -- Medium (docs) or P1 -- Critical (demo day).

## Prerequisites

- Node.js 20+ and pnpm (for Mintlify dev server)
- Go 1.22+ (for demo seed script)
- `psql` CLI (for verifying seed data)
- Docker running with `make docker-up` (for live demo)
- Python 3 with `openapi-spec-validator` and `pyyaml` (for validation)

## Architecture

### Deliverables

| File | Purpose | Format |
|------|---------|--------|
| `docs-site/openapi.json` | OpenAPI 3.1 spec (57 paths, 65 methods, 27 schemas) | JSON, read by Mintlify |
| `docs/openapi.yaml` | Same spec in YAML | YAML, for SDK codegen |
| `docs-site/api-reference/**/*.mdx` | 82 API reference pages with `openapi:` frontmatter | MDX |
| `docs-site/mint.json` | Mintlify config (20 nav groups, 103 pages) | JSON |
| `docs/settla.collection.json` | Postman collection (14 folders, 84 requests, all with tests) | Postman v2.1 |
| `docs/DEMO_SCRIPT.md` | 8-act demo walkthrough (25 min, 39 curl commands) | Markdown |
| `docs/API.md` | Standalone API reference (3,732 lines, 130 endpoints) | Markdown |
| `scripts/demo-seed.go` | Tenant scale seeder (10–100K tenants, tiered) | Go |
| `scripts/demo-cleanup.sh` | Removes seeded demo data | Bash |

### How MDX + OpenAPI Work Together

```
docs-site/mint.json          # Declares navigation + "openapi": "openapi.json"
         │
         ▼
docs-site/openapi.json       # Defines all endpoint schemas, params, responses
         │
         ▼
docs-site/api-reference/     # Each .mdx file has `openapi: "METHOD /path"` frontmatter
  transfers/                  # Mintlify auto-generates param tables from the spec
    create-transfer.mdx       # MDX body adds context, examples, warnings (not duplicated)
```

## Steps

### 1. Validate the Full Docs Suite

Run all validators in sequence:

```bash
# 1. OpenAPI spec validation (formal standard)
pip3 install openapi-spec-validator 2>/dev/null
python3 -c "
from openapi_spec_validator import validate
import json
with open('docs-site/openapi.json') as f:
    validate(json.load(f))
print('PASS: OpenAPI spec valid')
"

# 2. Mintlify-native OpenAPI check
cd docs-site && HOME=/tmp/npm-fix npx mintlify openapi-check openapi.json && cd ..

# 3. Mintlify broken-links check
cd docs-site && HOME=/tmp/npm-fix npx mintlify broken-links && cd ..

# 4. MDX frontmatter validation (all 103 files have title + openapi/api ref)
python3 -c "
import os, re, yaml
errors = []
for root, dirs, files in os.walk('docs-site'):
    for f in files:
        if not f.endswith('.mdx'): continue
        path = os.path.join(root, f)
        content = open(path).read()
        if not content.startswith('---'):
            errors.append(f'Missing frontmatter: {path}')
            continue
        parts = content.split('---', 2)
        fm = yaml.safe_load(parts[1])
        if not fm or 'title' not in fm:
            errors.append(f'Missing title: {path}')
        if len(parts) > 2 and len(parts[2].strip()) < 20:
            errors.append(f'Short body: {path}')
if errors:
    for e in errors: print(e)
    exit(1)
print(f'PASS: All {sum(1 for r,d,fs in os.walk(\"docs-site\") for f in fs if f.endswith(\".mdx\"))} MDX files valid')
"

# 5. Navigation completeness (every nav page exists, every MDX in nav)
python3 -c "
import json, os
config = json.load(open('docs-site/mint.json'))
nav_pages = set()
for g in config['navigation']:
    for p in g.get('pages', []):
        nav_pages.add(p)
        if not os.path.exists(f'docs-site/{p}.mdx'):
            print(f'FAIL: nav page missing: {p}')
            exit(1)
mdx = set()
for r, d, fs in os.walk('docs-site'):
    for f in fs:
        if f.endswith('.mdx'):
            mdx.add(os.path.relpath(os.path.join(r, f), 'docs-site').replace('.mdx', ''))
orphans = mdx - nav_pages
if orphans:
    print(f'FAIL: orphan MDX files: {orphans}')
    exit(1)
print(f'PASS: {len(nav_pages)} nav pages, 0 orphans')
"

# 6. MDX↔OpenAPI alignment (every openapi: ref resolves)
python3 -c "
import json, os, re
spec = json.load(open('docs-site/openapi.json'))
se = set()
for p, m in spec['paths'].items():
    for k in m:
        if k in ('get','post','put','delete','patch'):
            se.add(f'{k.upper()} {p}')
mr = set()
for r, d, fs in os.walk('docs-site/api-reference'):
    for f in fs:
        if not f.endswith('.mdx'): continue
        for l in open(os.path.join(r, f)):
            m2 = re.match(r'^openapi:\s*\"(.+)\"', l.strip())
            if m2: mr.add(m2.group(1))
if se != mr:
    print(f'FAIL: {len(se-mr)} orphan spec endpoints, {len(mr-se)} broken MDX refs')
    exit(1)
print(f'PASS: {len(se)} spec endpoints = {len(mr)} MDX refs, 0 mismatches')
"

# 7. Postman collection validation
python3 -c "
import json
j = json.load(open('docs/settla.collection.json'))
assert j['info']['schema'] == 'https://schema.getpostman.com/json/collection/v2.1.0/collection.json'
def count(items):
    c = 0
    for i in items:
        if 'item' in i: c += count(i['item'])
        elif 'request' in i: c += 1
    return c
print(f'PASS: Postman collection valid ({count(j[\"item\"])} requests)')
"

# 8. Go seed script compiles
go build -o /tmp/demo-seed-test scripts/demo-seed.go && echo "PASS: demo-seed.go compiles" && rm /tmp/demo-seed-test
go vet scripts/demo-seed.go && echo "PASS: go vet clean"

# 9. Cleanup script syntax
bash -n scripts/demo-cleanup.sh && echo "PASS: demo-cleanup.sh syntax valid"
```

Expected: all 9 checks print PASS.

### 2. Run the Docs Site Locally

```bash
cd docs-site
npx mintlify dev --port 3333
```

Open http://localhost:3333 in a browser. Verify:
- Navigation sidebar shows all 20 groups
- API reference pages show auto-generated param tables (from OpenAPI)
- Code examples render in tabbed `<CodeGroup>` blocks
- Error codes page has "Common Cause" and "Fix" columns
- Webhooks guide shows 4 language tabs (Node.js, Go, Python, PHP)

### 3. Prepare a Demo (Quick — 10 Tenants)

```bash
# Start infrastructure
make docker-up

# Wait for healthy (~60 seconds)
make docker-logs  # Watch for "ready" messages

# Seed 10 demo tenants (default)
go run scripts/demo-seed.go

# Verify
psql "postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable" \
  -c "SELECT id, name, slug, status FROM tenants ORDER BY created_at LIMIT 15;"
```

Then follow `docs/DEMO_SCRIPT.md` from Act 1.

### 4. Prepare a Scale Demo (20K Tenants)

```bash
# Seed 20K tenants (~2 minutes)
go run scripts/demo-seed.go --tenant-count=20000 --verbose

# Verify count
psql "postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable" \
  -c "SELECT COUNT(*) as total,
       COUNT(*) FILTER (WHERE metadata->>'tier' = 'enterprise') as enterprise,
       COUNT(*) FILTER (WHERE metadata->>'tier' = 'growth') as growth,
       COUNT(*) FILTER (WHERE metadata->>'tier' = 'starter') as starter
       FROM tenants WHERE metadata->>'seeded_by' = 'demo-seed';"
```

Expected: ~200 enterprise, ~2000 growth, ~17800 starter.

### 5. Clean Up After Demo

```bash
# Remove seeded data (preserves Lemfi + Fincra)
bash scripts/demo-cleanup.sh

# Or use the Go script's cleanup mode
go run scripts/demo-seed.go --cleanup

# Nuclear option: full reset
make docker-reset
```

### 6. Update Docs After API Changes

When adding a new endpoint:

```bash
# 1. Add to OpenAPI spec
vi docs-site/openapi.json

# 2. Create MDX page
cat > docs-site/api-reference/{group}/{name}.mdx << 'EOF'
---
title: "Endpoint Name"
openapi: "METHOD /v1/path"
---

Description of what this endpoint does and any important context.
EOF

# 3. Add to mint.json navigation
vi docs-site/mint.json  # Add page path to the correct group

# 4. Regenerate YAML spec
python3 -c "import json,yaml; yaml.dump(json.load(open('docs-site/openapi.json')),open('docs/openapi.yaml','w'),default_flow_style=False,sort_keys=False)"

# 5. Update Postman collection
vi docs/settla.collection.json  # Add request to correct folder

# 6. Re-run validation (Step 1)
```

### 7. Import Postman Collection

```bash
# In Postman: File → Import → Upload File → docs/settla.collection.json

# Set variables:
#   base_url: http://localhost:3000  (local) or https://sandbox.settla.io
#   api_key:  sk_test_lemfi_demo_key_001  (from seed)
#   ops_key:  settla-ops-demo-key-32chars-min

# Run the "Demo Flow" folder in order (16 requests, chained via variables)
```

## Troubleshooting

### Mintlify dev server won't start

**Cause:** npm cache permissions issue.

```bash
# Fix npm cache ownership
sudo chown -R $(whoami) ~/.npm

# Or use temporary HOME
HOME=/tmp/npm-fix npx mintlify dev --port 3333
```

### OpenAPI validation fails

**Cause:** Invalid $ref, missing required field, or schema mismatch.

```bash
# Get detailed error
python3 -c "
from openapi_spec_validator import validate
import json
try:
    validate(json.load(open('docs-site/openapi.json')))
except Exception as e:
    print(e)
"
```

Common fixes:
- Ensure all `$ref` targets exist in `components/schemas`
- All `required` arrays only list fields that exist in `properties`
- No `type: object` without `properties`

### MDX page shows "Endpoint not found" in Mintlify

**Cause:** The `openapi:` frontmatter doesn't match any path in the spec.

```bash
# Check what the MDX expects
grep "^openapi:" docs-site/api-reference/path/to/file.mdx

# Check what the spec has
python3 -c "
import json
spec = json.load(open('docs-site/openapi.json'))
for p in sorted(spec['paths']): print(p)
" | grep "keyword"
```

Fix: ensure the `openapi:` value matches exactly `"METHOD /v1/exact/path/{param}"`.

### Demo seed fails at connection

**Cause:** Database not running or wrong connection URL.

```bash
# Check Docker services
docker ps | grep postgres

# Test connectivity
psql "postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable" -c "SELECT 1;"

# Override URLs
go run scripts/demo-seed.go \
  --transfer-db-url="postgres://settla:settla@custom-host:5434/settla_transfer?sslmode=disable" \
  --ledger-db-url="postgres://settla:settla@custom-host:5433/settla_ledger?sslmode=disable" \
  --treasury-db-url="postgres://settla:settla@custom-host:5435/settla_treasury?sslmode=disable"
```

### Postman collection variables not chaining

**Cause:** Requests run out of order (variable set in request A, used in request B).

Fix: Run the "Demo Flow" folder using Postman's Collection Runner — it executes requests sequentially. Or manually run the prerequisite request first (e.g., Create Quote before Create Transfer).

## Validation Checklist (Pre-Release)

Run before every release that touches the API:

- [ ] `openapi-spec-validator` passes on `docs-site/openapi.json`
- [ ] `mintlify openapi-check openapi.json` passes
- [ ] `mintlify broken-links` passes
- [ ] All 103 MDX files have valid frontmatter
- [ ] All nav pages exist on disk, no orphan MDX files
- [ ] All `openapi:` MDX refs resolve to spec paths (0 mismatches)
- [ ] Postman collection parses as valid JSON with correct schema
- [ ] `go build scripts/demo-seed.go` compiles
- [ ] `bash -n scripts/demo-cleanup.sh` passes
- [ ] `docs/openapi.yaml` matches `docs-site/openapi.json`
- [ ] Mintlify dev server renders all 103 pages (HTTP 200)
- [ ] No stale state names (TREASURY_RESERVED, HOLD_CRYPTO, etc.)
