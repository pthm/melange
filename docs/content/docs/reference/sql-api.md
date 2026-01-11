---
title: SQL API
weight: 3
---

Melange generates SQL functions that can be called directly from any PostgreSQL client. This allows you to use Melange's authorization system from any language or framework without requiring the Go library.

## Overview

When you run `melange migrate`, Melange generates:

| Function | Purpose |
|----------|---------|
| `check_permission` | Check if a subject has a relation on an object |
| `list_accessible_objects` | List all objects a subject can access |
| `list_accessible_subjects` | List all subjects with access to an object |

These are the primary entry points. Internally, Melange generates specialized per-relation functions (e.g., `check_document_viewer`) that the dispatchers route to.

## check_permission

Checks whether a subject has a specific relation on an object.

### Signature

```sql
check_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_subject_type` | TEXT | Type of the subject (e.g., `'user'`, `'team'`) |
| `p_subject_id` | TEXT | ID of the subject |
| `p_relation` | TEXT | Relation to check (e.g., `'viewer'`, `'can_read'`) |
| `p_object_type` | TEXT | Type of the object (e.g., `'document'`, `'repository'`) |
| `p_object_id` | TEXT | ID of the object |

### Return Value

- `1` - Access granted
- `0` - Access denied

### Examples

```sql
-- Check if user 123 can view document 456
SELECT check_permission('user', '123', 'viewer', 'document', '456');

-- Use in a WHERE clause to filter accessible records
SELECT d.*
FROM documents d
WHERE check_permission('user', '123', 'viewer', 'document', d.id::text) = 1;

-- Use with CASE for conditional logic
SELECT
    d.id,
    d.title,
    CASE WHEN check_permission('user', '123', 'editor', 'document', d.id::text) = 1
         THEN true ELSE false END AS can_edit
FROM documents d
WHERE check_permission('user', '123', 'viewer', 'document', d.id::text) = 1;
```

## list_accessible_objects

Returns all object IDs that a subject has a specific relation on.

### Signature

```sql
list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT)
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_subject_type` | TEXT | Type of the subject |
| `p_subject_id` | TEXT | ID of the subject |
| `p_relation` | TEXT | Relation to check |
| `p_object_type` | TEXT | Type of objects to list |

### Return Value

Returns a table with a single column `object_id` containing all accessible object IDs.

### Examples

```sql
-- Get all documents user 123 can view
SELECT * FROM list_accessible_objects('user', '123', 'viewer', 'document');

-- Join with domain table to get full records
SELECT d.*
FROM documents d
JOIN list_accessible_objects('user', '123', 'viewer', 'document') a
    ON d.id::text = a.object_id;

-- Count accessible objects
SELECT COUNT(*) FROM list_accessible_objects('user', '123', 'viewer', 'document');
```

## list_accessible_subjects

Returns all subjects that have a specific relation on an object.

### Signature

```sql
list_accessible_subjects(
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT)
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_object_type` | TEXT | Type of the object |
| `p_object_id` | TEXT | ID of the object |
| `p_relation` | TEXT | Relation to check |
| `p_subject_type` | TEXT | Type of subjects to list |

### Return Value

Returns a table with a single column `subject_id` containing all subject IDs with access.

### Examples

```sql
-- Get all users who can view document 456
SELECT * FROM list_accessible_subjects('document', '456', 'viewer', 'user');

-- Join with users table to get full user records
SELECT u.*
FROM users u
JOIN list_accessible_subjects('document', '456', 'viewer', 'user') a
    ON u.id::text = a.subject_id;

-- Get all team members with access (userset filter)
-- Note: Use 'team#member' as subject_type to filter by userset
SELECT * FROM list_accessible_subjects('document', '456', 'viewer', 'team#member');
```

## Error Handling

### Error Code: M2002

The functions raise an exception with error code `M2002` when the permission resolution exceeds the depth limit (25 levels):

```sql
RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
```

This can occur with:
- Deeply nested parent relationships (tuple-to-userset chains)
- Complex userset chains exceeding 25 levels
- Cyclic permission structures

Handle this error in your application:

```sql
DO $$
BEGIN
    PERFORM check_permission('user', '123', 'viewer', 'document', '456');
EXCEPTION
    WHEN SQLSTATE 'M2002' THEN
        RAISE NOTICE 'Permission resolution too complex';
END;
$$;
```

### Unknown Type/Relation

When an unknown object type or relation is queried, the functions return:
- `check_permission`: `0` (denied)
- `list_accessible_objects`: Empty result set
- `list_accessible_subjects`: Empty result set

No error is raised for unknown types/relations.

## Usage from Different Languages

### Python (psycopg2)

```python
import psycopg2

conn = psycopg2.connect("postgresql://localhost/mydb")
cur = conn.cursor()

# Check permission
cur.execute(
    "SELECT check_permission(%s, %s, %s, %s, %s)",
    ('user', '123', 'viewer', 'document', '456')
)
allowed = cur.fetchone()[0] == 1

# List accessible objects
cur.execute(
    "SELECT * FROM list_accessible_objects(%s, %s, %s, %s)",
    ('user', '123', 'viewer', 'document')
)
accessible_ids = [row[0] for row in cur.fetchall()]
```

### Node.js (pg)

```javascript
const { Pool } = require('pg');
const pool = new Pool({ connectionString: 'postgresql://localhost/mydb' });

// Check permission
const { rows } = await pool.query(
  'SELECT check_permission($1, $2, $3, $4, $5)',
  ['user', '123', 'viewer', 'document', '456']
);
const allowed = rows[0].check_permission === 1;

// List accessible objects
const result = await pool.query(
  'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
  ['user', '123', 'viewer', 'document']
);
const accessibleIds = result.rows.map(row => row.object_id);
```

### Ruby (pg gem)

```ruby
require 'pg'

conn = PG.connect(dbname: 'mydb')

# Check permission
result = conn.exec_params(
  'SELECT check_permission($1, $2, $3, $4, $5)',
  ['user', '123', 'viewer', 'document', '456']
)
allowed = result[0]['check_permission'] == '1'

# List accessible objects
result = conn.exec_params(
  'SELECT * FROM list_accessible_objects($1, $2, $3, $4)',
  ['user', '123', 'viewer', 'document']
)
accessible_ids = result.map { |row| row['object_id'] }
```

### Java (JDBC)

```java
import java.sql.*;

Connection conn = DriverManager.getConnection("jdbc:postgresql://localhost/mydb");

// Check permission
PreparedStatement ps = conn.prepareStatement(
    "SELECT check_permission(?, ?, ?, ?, ?)"
);
ps.setString(1, "user");
ps.setString(2, "123");
ps.setString(3, "viewer");
ps.setString(4, "document");
ps.setString(5, "456");

ResultSet rs = ps.executeQuery();
rs.next();
boolean allowed = rs.getInt(1) == 1;

// List accessible objects
ps = conn.prepareStatement(
    "SELECT * FROM list_accessible_objects(?, ?, ?, ?)"
);
ps.setString(1, "user");
ps.setString(2, "123");
ps.setString(3, "viewer");
ps.setString(4, "document");

rs = ps.executeQuery();
List<String> accessibleIds = new ArrayList<>();
while (rs.next()) {
    accessibleIds.add(rs.getString("object_id"));
}
```

### Rust (tokio-postgres)

```rust
use tokio_postgres::NoTls;

let (client, connection) = tokio_postgres::connect(
    "postgresql://localhost/mydb", NoTls
).await?;

// Check permission
let row = client.query_one(
    "SELECT check_permission($1, $2, $3, $4, $5)",
    &[&"user", &"123", &"viewer", &"document", &"456"]
).await?;
let allowed: i32 = row.get(0);
let has_access = allowed == 1;

// List accessible objects
let rows = client.query(
    "SELECT * FROM list_accessible_objects($1, $2, $3, $4)",
    &[&"user", &"123", &"viewer", &"document"]
).await?;
let accessible_ids: Vec<String> = rows.iter()
    .map(|row| row.get("object_id"))
    .collect();
```

## Specialized Functions

In addition to the dispatcher functions, Melange generates specialized functions for each type/relation pair. These are called internally by the dispatchers but can also be called directly if you know the exact type and relation:

| Pattern | Function Name |
|---------|---------------|
| Check | `check_{type}_{relation}(subject_type, subject_id, object_id, visited)` |
| List objects | `list_{type}_{relation}_objects(subject_type, subject_id)` |
| List subjects | `list_{type}_{relation}_subjects(object_id, subject_type)` |

For example, for a `document` type with a `viewer` relation:

```sql
-- Direct specialized function call (slightly faster, bypasses dispatcher)
SELECT check_document_viewer('user', '123', '456', ARRAY[]::TEXT[]);

-- List objects using specialized function
SELECT * FROM list_document_viewer_objects('user', '123');

-- List subjects using specialized function
SELECT * FROM list_document_viewer_subjects('456', 'user');
```

{{< callout type="info" >}}
**Note**: Specialized functions include a `p_visited` parameter used for cycle detection in recursive patterns. When calling directly, pass `ARRAY[]::TEXT[]` for this parameter.
{{< /callout >}}

## Performance Considerations

### Use List Functions for Batch Operations

Instead of calling `check_permission` for each object:

```sql
-- Inefficient: N function calls
SELECT d.* FROM documents d
WHERE check_permission('user', '123', 'viewer', 'document', d.id::text) = 1;
```

Use list functions with a JOIN:

```sql
-- Efficient: 1 function call + JOIN
SELECT d.* FROM documents d
JOIN list_accessible_objects('user', '123', 'viewer', 'document') a
    ON d.id::text = a.object_id;
```

### Transaction Consistency

All functions are marked `STABLE`, meaning they see a consistent snapshot of the database within a transaction. Permission checks within the same transaction will see uncommitted changes to the `melange_tuples` view.

```sql
BEGIN;
-- Insert new tuple (via domain table that feeds the view)
INSERT INTO team_members (user_id, team_id, role) VALUES ('123', '456', 'member');

-- Permission check sees the uncommitted row
SELECT check_permission('user', '123', 'member', 'team', '456');
-- Returns 1

ROLLBACK;
-- Now returns 0
```

### Index Recommendations

See [Tuples View](../concepts/tuples-view.md#performance-optimization) for indexing strategies that optimize the SQL functions.
