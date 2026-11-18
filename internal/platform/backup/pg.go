package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
)

// DBMeta describes the Postgres tables, partitions, sequences, and foreign keys
// captured in a backup snapshot.
type DBMeta struct {
	Schemas        []string         `json:"schemas"`
	SchemaVersions map[string]int64 `json:"schema_versions,omitempty"`
	KeyIDs         []string         `json:"key_ids,omitempty"`
	Tables         []TableMeta      `json:"tables"`
	Partitions     []PartitionMeta  `json:"partitions,omitempty"`
	Sequences      []SequenceMeta   `json:"sequences,omitempty"`
	ForeignKeys    []ForeignKeyMeta `json:"foreign_keys,omitempty"`
}

// TableMeta describes one dumped table and its non-generated columns.
type TableMeta struct {
	Schema    string   `json:"schema"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // "r" or "p"
	Columns   []string `json:"columns"`
	PKColumns []string `json:"pk_columns,omitempty"`
}

// PartitionMeta records one child partition of a partitioned table.
type PartitionMeta struct {
	ParentSchema string `json:"parent_schema"`
	ParentTable  string `json:"parent_table"`
	Schema       string `json:"schema"`
	Name         string `json:"name"`
	Bound        string `json:"bound"`
}

// SequenceMeta records the last value and is_called state of one sequence.
type SequenceMeta struct {
	Schema    string `json:"schema"`
	Name      string `json:"name"`
	LastValue int64  `json:"last_value"`
	IsCalled  bool   `json:"is_called"`
}

// ForeignKeyMeta records a foreign-key dependency from one table to another.
type ForeignKeyMeta struct {
	FromSchema string `json:"from_schema"`
	FromTable  string `json:"from_table"`
	ToSchema   string `json:"to_schema"`
	ToTable    string `json:"to_table"`
}

var (
	partBoundRangeRE = regexp.MustCompile(`^FOR VALUES FROM \('([0-9]{4}-[0-9]{2}-[0-9]{2}(?:[ T][0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}(?::?[0-9]{2})?)?)?)'\) TO \('([0-9]{4}-[0-9]{2}-[0-9]{2}(?:[ T][0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}(?::?[0-9]{2})?)?)?)'\)$`)
	identRE          = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	keyIDRE          = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
)

func checkPrivileges(ctx context.Context, pool *pgxpool.Pool) error {
	var role string
	var privileged bool
	if err := pool.QueryRow(ctx, `
		SELECT rolname, rolsuper OR rolbypassrls
		FROM pg_roles
		WHERE rolname = current_user`,
	).Scan(&role, &privileged); err != nil {
		return fmt.Errorf("backup: check database role privileges: %w", err)
	}
	if !privileged {
		return fmt.Errorf("backup: database role %q requires SUPERUSER or BYPASSRLS privilege for RLS-enforced tables", role)
	}
	return nil
}

func knownSchemaSet() map[string]bool {
	m := make(map[string]bool, len(migrate.KnownSchemas))
	for _, s := range migrate.KnownSchemas {
		m[s] = true
	}
	return m
}

func dumpDB(ctx context.Context, pool *pgxpool.Pool, tw *tar.Writer, entries *[]EntryChecksum, tmpDir string) (DBMeta, map[string]int64, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return DBMeta{}, nil, fmt.Errorf("backup: begin read-only snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	schemas, err := queryExistingSchemas(ctx, tx, migrate.KnownSchemas)
	if err != nil {
		return DBMeta{}, nil, err
	}
	versions, err := querySchemaVersionsTx(ctx, tx, schemas)
	if err != nil {
		return DBMeta{}, nil, err
	}

	tables, partitions, err := queryTablesAndPartitions(ctx, tx, schemas)
	if err != nil {
		return DBMeta{}, nil, err
	}
	fks, err := queryForeignKeys(ctx, tx, schemas)
	if err != nil {
		return DBMeta{}, nil, err
	}
	seqs, err := querySequences(ctx, tx, schemas)
	if err != nil {
		return DBMeta{}, nil, err
	}
	keyIDs, err := querySealedKeyIDs(ctx, tx, tables)
	if err != nil {
		return DBMeta{}, nil, err
	}

	orderedTables := topoSortTables(tables, fks)
	for _, tbl := range orderedTables {
		if len(tbl.Columns) == 0 {
			continue
		}
		entryPath := fmt.Sprintf("%s%s.%s.copy", dbTablesPrefix, tbl.Schema, tbl.Name)
		sum, size, err := copyTableToTar(ctx, tx, tw, tbl, entryPath, tmpDir)
		if err != nil {
			return DBMeta{}, nil, err
		}
		*entries = append(*entries, EntryChecksum{
			Path:   entryPath,
			SHA256: sum,
			Size:   size,
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return DBMeta{}, nil, fmt.Errorf("backup: finish snapshot transaction: %w", err)
	}

	meta := DBMeta{
		Schemas:        schemas,
		SchemaVersions: versions,
		KeyIDs:         keyIDs,
		Tables:         tables,
		Partitions:     partitions,
		Sequences:      seqs,
		ForeignKeys:    fks,
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return DBMeta{}, nil, fmt.Errorf("backup: marshal db metadata: %w", err)
	}
	metaSum, metaSize, err := writeBytesToTar(tw, dbMetaEntryName, metaBytes)
	if err != nil {
		return DBMeta{}, nil, err
	}
	*entries = append(*entries, EntryChecksum{
		Path:   dbMetaEntryName,
		SHA256: metaSum,
		Size:   metaSize,
	})
	return meta, versions, nil
}

func querySealedKeyIDs(ctx context.Context, tx pgx.Tx, tables []TableMeta) ([]string, error) {
	seen := make(map[string]bool)
	hasTableCol := func(schema, table, col string) bool {
		for _, t := range tables {
			if t.Schema == schema && t.Name == table {
				return slices.Contains(t.Columns, col)
			}
		}
		return false
	}

	if hasTableCol("gateway", "credentials", "key_id") {
		rows, err := tx.Query(ctx, `SELECT DISTINCT key_id FROM gateway.credentials WHERE key_id <> ''`)
		if err != nil {
			return nil, fmt.Errorf("backup: scan gateway credential key IDs: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("backup: scan gateway credential key ID: %w", err)
			}
			seen[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("backup: scan gateway credential key IDs: %w", err)
		}
		rows.Close()
	}

	if hasTableCol("identity", "users", "password_hash") {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT split_part(password_hash, ':', 1)
			FROM identity.users
			WHERE password_hash ~ '^[A-Za-z0-9_]{1,32}:[^:]+:[^:]+$'`)
		if err != nil {
			return nil, fmt.Errorf("backup: scan identity password key IDs: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("backup: scan identity password key ID: %w", err)
			}
			seen[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("backup: scan identity password key IDs: %w", err)
		}
		rows.Close()
	}

	if hasTableCol("identity", "users", "email_hash_key_id") {
		rows, err := tx.Query(ctx, `
			SELECT split_part(email, ':', 1) FROM identity.users
			WHERE email_hash_key_id IS NOT NULL AND email ~ '^[A-Za-z0-9_]{1,32}:[^:]+:[^:]+$'
			UNION
			SELECT email_hash_key_id FROM identity.users
			WHERE email_hash_key_id ~ '^[A-Za-z0-9_]{1,32}$'`)
		if err != nil {
			return nil, fmt.Errorf("backup: scan identity email key IDs: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("backup: scan identity email key ID: %w", err)
			}
			seen[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("backup: scan identity email key IDs: %w", err)
		}
		rows.Close()
	}

	if len(seen) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func queryExistingSchemas(ctx context.Context, tx pgx.Tx, allow []string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT nspname
		FROM pg_namespace
		WHERE nspname = ANY($1)
		ORDER BY nspname`, allow)
	if err != nil {
		return nil, fmt.Errorf("backup: list schemas: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("backup: scan schema: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list schemas: %w", err)
	}
	return out, nil
}

func querySchemaVersionsTx(ctx context.Context, tx pgx.Tx, schemas []string) (map[string]int64, error) {
	versions := make(map[string]int64)
	if len(schemas) == 0 {
		return versions, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT table_schema
		FROM information_schema.tables
		WHERE table_name = 'goose_db_version'
		  AND table_schema = ANY($1)
		ORDER BY table_schema`, schemas)
	if err != nil {
		return nil, fmt.Errorf("backup: list goose version tables: %w", err)
	}
	var present []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, fmt.Errorf("backup: scan version schema: %w", err)
		}
		present = append(present, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("backup: list goose version tables: %w", err)
	}
	rows.Close()

	allowed := knownSchemaSet()
	for _, schema := range present {
		if !allowed[schema] {
			continue
		}
		var ver int64
		q := "SELECT COALESCE(max(version_id), 0) FROM " + pgx.Identifier{schema, "goose_db_version"}.Sanitize() + " WHERE is_applied AND version_id > 0"
		if err := tx.QueryRow(ctx, q).Scan(&ver); err != nil {
			return nil, fmt.Errorf("backup: read schema version %s: %w", schema, err)
		}
		versions[schema] = ver
	}
	return versions, nil
}

func queryTablesAndPartitions(ctx context.Context, tx pgx.Tx, schemas []string) ([]TableMeta, []PartitionMeta, error) {
	if len(schemas) == 0 {
		return nil, nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT n.nspname, c.relname, c.oid, c.relkind::text
		FROM pg_class AS c
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY($1)
		  AND c.relkind IN ('r', 'p')
		  AND NOT c.relispartition
		  AND c.relname NOT IN ('goose_db_version', 'river_migration')
		ORDER BY n.nspname, c.relname`, schemas)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: list tables: %w", err)
	}
	type rawTable struct {
		schema string
		name   string
		oid    uint32
		kind   string
	}
	var raw []rawTable
	for rows.Next() {
		var r rawTable
		if err := rows.Scan(&r.schema, &r.name, &r.oid, &r.kind); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("backup: scan table: %w", err)
		}
		raw = append(raw, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, fmt.Errorf("backup: list tables: %w", err)
	}
	rows.Close()

	tables := make([]TableMeta, 0, len(raw))
	var partitions []PartitionMeta
	for _, r := range raw {
		cols, err := queryTableColumns(ctx, tx, r.oid)
		if err != nil {
			return nil, nil, err
		}
		pks, err := queryPrimaryKeyColumns(ctx, tx, r.oid)
		if err != nil {
			return nil, nil, err
		}
		tables = append(tables, TableMeta{
			Schema:    r.schema,
			Name:      r.name,
			Kind:      r.kind,
			Columns:   cols,
			PKColumns: pks,
		})
		if r.kind == "p" {
			parts, err := queryPartitions(ctx, tx, r.schema, r.name, r.oid)
			if err != nil {
				return nil, nil, err
			}
			partitions = append(partitions, parts...)
		}
	}
	return tables, partitions, nil
}

func queryTableColumns(ctx context.Context, tx pgx.Tx, relOID uint32) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.attname
		FROM pg_attribute AS a
		WHERE a.attrelid = $1
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		  AND a.attgenerated = ''
		ORDER BY a.attnum`, relOID)
	if err != nil {
		return nil, fmt.Errorf("backup: list columns: %w", err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("backup: scan column: %w", err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list columns: %w", err)
	}
	return cols, nil
}

func queryPrimaryKeyColumns(ctx context.Context, tx pgx.Tx, relOID uint32) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.attname
		FROM pg_index AS i
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute AS a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE i.indrelid = $1
		  AND i.indisprimary
		ORDER BY k.ord`, relOID)
	if err != nil {
		return nil, fmt.Errorf("backup: list primary key: %w", err)
	}
	defer rows.Close()

	var pks []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("backup: scan primary key column: %w", err)
		}
		pks = append(pks, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list primary key: %w", err)
	}
	return pks, nil
}

func queryPartitions(ctx context.Context, tx pgx.Tx, parentSchema, parentTable string, parentOID uint32) ([]PartitionMeta, error) {
	rows, err := tx.Query(ctx, `
		SELECT n.nspname, c.relname, pg_get_expr(c.relpartbound, c.oid)
		FROM pg_inherits AS i
		JOIN pg_class AS c ON c.oid = i.inhrelid
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE i.inhparent = $1
		ORDER BY n.nspname, c.relname`, parentOID)
	if err != nil {
		return nil, fmt.Errorf("backup: list partitions for %s.%s: %w", parentSchema, parentTable, err)
	}
	defer rows.Close()

	var out []PartitionMeta
	for rows.Next() {
		var p PartitionMeta
		p.ParentSchema = parentSchema
		p.ParentTable = parentTable
		if err := rows.Scan(&p.Schema, &p.Name, &p.Bound); err != nil {
			return nil, fmt.Errorf("backup: scan partition: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list partitions: %w", err)
	}
	return out, nil
}

func queryForeignKeys(ctx context.Context, tx pgx.Tx, schemas []string) ([]ForeignKeyMeta, error) {
	if len(schemas) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT
			ns_from.nspname, cls_from.relname,
			ns_to.nspname, cls_to.relname
		FROM pg_constraint AS con
		JOIN pg_class AS cls_from ON cls_from.oid = con.conrelid
		JOIN pg_namespace AS ns_from ON ns_from.oid = cls_from.relnamespace
		JOIN pg_class AS cls_to ON cls_to.oid = con.confrelid
		JOIN pg_namespace AS ns_to ON ns_to.oid = cls_to.relnamespace
		WHERE con.contype = 'f'
		  AND ns_from.nspname = ANY($1)
		  AND ns_to.nspname = ANY($1)
		ORDER BY 1, 2, 3, 4`, schemas)
	if err != nil {
		return nil, fmt.Errorf("backup: list foreign keys: %w", err)
	}
	defer rows.Close()

	var out []ForeignKeyMeta
	for rows.Next() {
		var fk ForeignKeyMeta
		if err := rows.Scan(&fk.FromSchema, &fk.FromTable, &fk.ToSchema, &fk.ToTable); err != nil {
			return nil, fmt.Errorf("backup: scan foreign key: %w", err)
		}
		out = append(out, fk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backup: list foreign keys: %w", err)
	}
	return out, nil
}

func querySequences(ctx context.Context, tx pgx.Tx, schemas []string) ([]SequenceMeta, error) {
	if len(schemas) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT n.nspname, c.relname
		FROM pg_class AS c
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY($1)
		  AND c.relkind = 'S'
		  AND c.relname <> 'goose_db_version_id_seq'
		ORDER BY n.nspname, c.relname`, schemas)
	if err != nil {
		return nil, fmt.Errorf("backup: list sequences: %w", err)
	}
	type seqRef struct{ schema, name string }
	var refs []seqRef
	for rows.Next() {
		var r seqRef
		if err := rows.Scan(&r.schema, &r.name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("backup: scan sequence: %w", err)
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("backup: list sequences: %w", err)
	}
	rows.Close()

	allowed := knownSchemaSet()
	out := make([]SequenceMeta, 0, len(refs))
	for _, r := range refs {
		if !allowed[r.schema] {
			continue
		}
		var lastVal int64
		var isCalled bool
		q := "SELECT last_value, is_called FROM " + pgx.Identifier{r.schema, r.name}.Sanitize()
		if err := tx.QueryRow(ctx, q).Scan(&lastVal, &isCalled); err != nil {
			return nil, fmt.Errorf("backup: read sequence %s.%s: %w", r.schema, r.name, err)
		}
		out = append(out, SequenceMeta{
			Schema:    r.schema,
			Name:      r.name,
			LastValue: lastVal,
			IsCalled:  isCalled,
		})
	}
	return out, nil
}

func copyTableToTar(ctx context.Context, tx pgx.Tx, tw *tar.Writer, tbl TableMeta, entryPath, tmpDir string) (string, int64, error) {
	tmp, err := os.CreateTemp(tmpDir, "table-*.copy")
	if err != nil {
		return "", 0, fmt.Errorf("backup: create temp copy file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	cols := make([]string, len(tbl.Columns))
	for i, c := range tbl.Columns {
		cols[i] = pgx.Identifier{c}.Sanitize()
	}
	selectSQL := "SELECT " + strings.Join(cols, ", ") + " FROM " + pgx.Identifier{tbl.Schema, tbl.Name}.Sanitize()
	if len(tbl.PKColumns) > 0 {
		pks := make([]string, len(tbl.PKColumns))
		for i, pk := range tbl.PKColumns {
			pks[i] = pgx.Identifier{pk}.Sanitize()
		}
		selectSQL += " ORDER BY " + strings.Join(pks, ", ")
	}
	copySQL := "COPY (" + selectSQL + ") TO STDOUT (FORMAT text)"

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	if _, err := tx.Conn().PgConn().CopyTo(ctx, mw, copySQL); err != nil {
		return "", 0, fmt.Errorf("backup: copy out %s.%s: %w", tbl.Schema, tbl.Name, err)
	}
	size, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		return "", 0, fmt.Errorf("backup: measure copy file %s.%s: %w", tbl.Schema, tbl.Name, err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", 0, fmt.Errorf("backup: rewind copy file %s.%s: %w", tbl.Schema, tbl.Name, err)
	}
	hdr := &tar.Header{
		Name:     entryPath,
		Typeflag: tar.TypeReg,
		Size:     size,
		Mode:     0o600,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return "", 0, fmt.Errorf("backup: write tar header %s: %w", entryPath, err)
	}
	if size > 0 {
		if _, err := io.CopyN(tw, tmp, size); err != nil {
			return "", 0, fmt.Errorf("backup: write tar data %s: %w", entryPath, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func writeBytesToTar(tw *tar.Writer, entryPath string, data []byte) (string, int64, error) {
	sum := sha256.Sum256(data)
	size := int64(len(data))
	hdr := &tar.Header{
		Name:     entryPath,
		Typeflag: tar.TypeReg,
		Size:     size,
		Mode:     0o600,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return "", 0, fmt.Errorf("backup: write tar header %s: %w", entryPath, err)
	}
	if _, err := tw.Write(data); err != nil {
		return "", 0, fmt.Errorf("backup: write tar bytes %s: %w", entryPath, err)
	}
	return hex.EncodeToString(sum[:]), size, nil
}

// PrepareSchemasFunc prepares database schemas at specific migration versions
// before table data is loaded into a fresh or version-mismatched target.
type PrepareSchemasFunc func(ctx context.Context, owner *pgxpool.Pool, versions map[string]int64) error

// MigrateForwardFunc runs forward migrations to the binary's current head.
type MigrateForwardFunc func(ctx context.Context, owner *pgxpool.Pool) error

// errMigrateAfterCommit marks a restore whose data committed on the live target
// and whose forward migrations then failed; the staged volumes must be swapped
// in so database and files stay in step.
var errMigrateAfterCommit = errors.New("backup: database and volumes were restored from the archive, but forward migrations failed on the live database after succeeding in staging; run `upmo migrate` before starting brain")

func migrateAfterCommitErr(err error) error {
	return fmt.Errorf("%w: %w", errMigrateAfterCommit, err)
}

// emptyTargetFailure returns err after dropping the known schemas, so a failed
// live restore onto an empty target leaves the target exactly as it was.
func emptyTargetFailure(ctx context.Context, pool *pgxpool.Pool, err error) error {
	if dropErr := dropKnownSchemas(ctx, pool); dropErr != nil {
		return fmt.Errorf("%w (dropping schemas to return target to empty also failed: %v)", err, dropErr)
	}
	return err
}

// afterDropFailure wraps err from a live step that failed after the known
// schemas were dropped; the old data is gone, so the restore must be re-run.
func afterDropFailure(err error) error {
	return fmt.Errorf("backup: live schemas were dropped after the staging rehearsal passed, but the live restore failed; the database holds no restored data, re-run the restore: %w", err)
}

func restoreDB(
	ctx context.Context,
	pool *pgxpool.Pool,
	meta DBMeta,
	manifest Manifest,
	payloadTarPath string,
	tableSizes map[string]int64,
	prepareSchemas PrepareSchemasFunc,
	migrateForward MigrateForwardFunc,
) (int, int64, error) {
	if err := validateDBMeta(meta); err != nil {
		return 0, 0, err
	}
	currentVersions, err := migrate.SchemaVersions(ctx, pool)
	if err != nil {
		return 0, 0, fmt.Errorf("backup: read target schema versions: %w", err)
	}
	_, hasAudit := manifest.SchemaVersions[audit.Schema]

	// Rehearse the full restore (schema prepare, table load, forward migrations,
	// audit check) in a throwaway staging database before any live statement.
	if len(manifest.SchemaVersions) > 0 && prepareSchemas == nil {
		return 0, 0, errors.New("backup: PrepareSchemas callback is required to rehearse the restore in staging")
	}
	if err := verifyRestoreInStagingDB(ctx, pool, meta, manifest, payloadTarPath, tableSizes, prepareSchemas, migrateForward, hasAudit); err != nil {
		return 0, 0, err
	}

	switch {
	case len(currentVersions) == 0:
		// Empty target DB: prepare schemas at the backup head, load and verify in one tx,
		// then migrate forward; any live failure returns the target to empty.
		if len(manifest.SchemaVersions) > 0 {
			if err := prepareSchemas(ctx, pool, manifest.SchemaVersions); err != nil {
				return 0, 0, emptyTargetFailure(ctx, pool, fmt.Errorf("backup: prepare target schemas: %w", err))
			}
		}
		headPos, err := restoreTablesTx(ctx, pool, meta, payloadTarPath, tableSizes, hasAudit)
		if err != nil {
			return 0, 0, emptyTargetFailure(ctx, pool, err)
		}
		if migrateForward != nil {
			if err := migrateForward(ctx, pool); err != nil {
				return 0, 0, emptyTargetFailure(ctx, pool, fmt.Errorf("backup: forward migrations: %w", err))
			}
		}
		return len(meta.Tables), headPos, nil

	case maps.Equal(currentVersions, manifest.SchemaVersions):
		// Target at same schema head: never drop schemas; truncate, load, setval, and verify inside one tx.
		headPos, err := restoreTablesTx(ctx, pool, meta, payloadTarPath, tableSizes, hasAudit)
		if err != nil {
			return 0, 0, err
		}
		if migrateForward != nil {
			if err := migrateForward(ctx, pool); err != nil {
				return 0, 0, migrateAfterCommitErr(err)
			}
		}
		return len(meta.Tables), headPos, nil

	default:
		// Populated target at a different schema head: drop known schemas, rebuild at
		// the backup head, then load and migrate forward (already rehearsed above).
		// A failure past the drop cannot bring the old data back; say so.
		if err := dropKnownSchemas(ctx, pool); err != nil {
			return 0, 0, err
		}
		if len(manifest.SchemaVersions) > 0 {
			if err := prepareSchemas(ctx, pool, manifest.SchemaVersions); err != nil {
				return 0, 0, afterDropFailure(fmt.Errorf("prepare target schemas: %w", err))
			}
		}
		headPos, err := restoreTablesTx(ctx, pool, meta, payloadTarPath, tableSizes, hasAudit)
		if err != nil {
			return 0, 0, afterDropFailure(err)
		}
		if migrateForward != nil {
			if err := migrateForward(ctx, pool); err != nil {
				return 0, 0, migrateAfterCommitErr(err)
			}
		}
		return len(meta.Tables), headPos, nil
	}
}

func verifyRestoreInStagingDB(
	ctx context.Context,
	pool *pgxpool.Pool,
	meta DBMeta,
	manifest Manifest,
	payloadTarPath string,
	tableSizes map[string]int64,
	prepareSchemas PrepareSchemasFunc,
	migrateForward MigrateForwardFunc,
	hasAudit bool,
) error {
	var randBuf [8]byte
	if _, err := rand.Read(randBuf[:]); err != nil {
		return fmt.Errorf("backup: generate staging database name: %w", err)
	}
	stageDB := "upmo_restore_stage_" + hex.EncodeToString(randBuf[:])
	quotedDB := pgx.Identifier{stageDB}.Sanitize()

	if _, err := pool.Exec(ctx, "CREATE DATABASE "+quotedDB); err != nil {
		return fmt.Errorf("backup: create staging database %s before modifying live target: %w", stageDB, err)
	}
	var stagePool *pgxpool.Pool
	defer func() {
		if stagePool != nil {
			stagePool.Close()
		}
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = pool.Exec(dropCtx, "DROP DATABASE IF EXISTS "+quotedDB+" WITH (FORCE)")
	}()

	cfg := pool.Config().Copy()
	cfg.ConnConfig.Database = stageDB
	var err error
	stagePool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("backup: connect staging database %s: %w", stageDB, err)
	}

	if len(manifest.SchemaVersions) > 0 {
		if err := prepareSchemas(ctx, stagePool, manifest.SchemaVersions); err != nil {
			return fmt.Errorf("backup: staging prepare schemas: %w", err)
		}
	}
	if _, err := restoreTablesTx(ctx, stagePool, meta, payloadTarPath, tableSizes, hasAudit); err != nil {
		return fmt.Errorf("backup: staging restore tables: %w", err)
	}
	if migrateForward != nil {
		if err := migrateForward(ctx, stagePool); err != nil {
			return fmt.Errorf("backup: staging forward migrations: %w", err)
		}
	}
	if hasAudit {
		res, err := audit.Verify(ctx, stagePool, 0, 0)
		if err != nil {
			return fmt.Errorf("backup: staging verify audit chain: %w", err)
		}
		if !res.OK {
			return fmt.Errorf("backup: staging audit chain verification failed at position %d (%s)", res.Position, res.Reason)
		}
	}
	return nil
}

func validateDBMeta(meta DBMeta) error {
	allowed := knownSchemaSet()
	for _, s := range meta.Schemas {
		if !allowed[s] {
			return fmt.Errorf("backup: unknown schema %q in metadata", s)
		}
	}
	for s, v := range meta.SchemaVersions {
		if !allowed[s] {
			return fmt.Errorf("backup: unknown schema %q in metadata schema_versions", s)
		}
		if v < 0 {
			return fmt.Errorf("backup: invalid negative schema version %d for %q", v, s)
		}
	}
	for _, id := range meta.KeyIDs {
		if !keyIDRE.MatchString(id) {
			return fmt.Errorf("backup: invalid key_id %q in metadata", id)
		}
	}
	for _, t := range meta.Tables {
		if !allowed[t.Schema] || !identRE.MatchString(t.Name) {
			return fmt.Errorf("backup: invalid table %s.%s in metadata", t.Schema, t.Name)
		}
		if len(t.Columns) == 0 {
			return fmt.Errorf("backup: table %s.%s has no columns", t.Schema, t.Name)
		}
		for _, c := range t.Columns {
			if !identRE.MatchString(c) {
				return fmt.Errorf("backup: invalid column %s.%s.%s in metadata", t.Schema, t.Name, c)
			}
		}
	}
	for _, p := range meta.Partitions {
		if !allowed[p.ParentSchema] || !identRE.MatchString(p.ParentTable) || !allowed[p.Schema] || !identRE.MatchString(p.Name) {
			return fmt.Errorf("backup: invalid partition %s.%s in metadata", p.Schema, p.Name)
		}
		if p.Bound != "DEFAULT" && !partBoundRangeRE.MatchString(p.Bound) {
			return fmt.Errorf("backup: invalid partition bound %q for %s.%s", p.Bound, p.Schema, p.Name)
		}
	}
	for _, seq := range meta.Sequences {
		if !allowed[seq.Schema] || !identRE.MatchString(seq.Name) {
			return fmt.Errorf("backup: invalid sequence %s.%s in metadata", seq.Schema, seq.Name)
		}
	}
	return nil
}

func dropKnownSchemas(ctx context.Context, pool *pgxpool.Pool) error {
	for i := len(migrate.KnownSchemas) - 1; i >= 0; i-- {
		s := migrate.KnownSchemas[i]
		stmt := "DROP SCHEMA IF EXISTS " + pgx.Identifier{s}.Sanitize() + " CASCADE"
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("backup: drop schema %s: %w", s, err)
		}
	}
	return nil
}

func restoreTablesTx(ctx context.Context, pool *pgxpool.Pool, meta DBMeta, payloadTarPath string, tableSizes map[string]int64, verifyAudit bool) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("backup: begin restore transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, part := range meta.Partitions {
		stmt := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s %s",
			pgx.Identifier{part.Schema, part.Name}.Sanitize(),
			pgx.Identifier{part.ParentSchema, part.ParentTable}.Sanitize(),
			validatedPartitionBound(part.Bound),
		)
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return 0, fmt.Errorf("backup: ensure partition %s.%s: %w", part.Schema, part.Name, err)
		}
	}

	if err := enterReplicaOrDeferredMode(ctx, tx); err != nil {
		return 0, err
	}

	for _, tbl := range meta.Tables {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+pgx.Identifier{tbl.Schema, tbl.Name}.Sanitize()+" DISABLE TRIGGER USER"); err != nil {
			return 0, fmt.Errorf("backup: disable triggers on %s.%s: %w", tbl.Schema, tbl.Name, err)
		}
	}

	if len(meta.Tables) > 0 {
		idents := make([]string, len(meta.Tables))
		for i, tbl := range meta.Tables {
			idents[i] = pgx.Identifier{tbl.Schema, tbl.Name}.Sanitize()
		}
		if _, err := tx.Exec(ctx, "TRUNCATE "+strings.Join(idents, ", ")+" CASCADE"); err != nil {
			return 0, fmt.Errorf("backup: truncate tables: %w", err)
		}
	}

	if err := copyTablesFromTarTx(ctx, tx, meta.Tables, payloadTarPath, tableSizes); err != nil {
		return 0, err
	}

	for _, seq := range meta.Sequences {
		reg := pgx.Identifier{seq.Schema, seq.Name}.Sanitize()
		if _, err := tx.Exec(ctx, `SELECT setval($1::regclass, $2, $3)`, reg, seq.LastValue, seq.IsCalled); err != nil {
			return 0, fmt.Errorf("backup: restore sequence %s.%s: %w", seq.Schema, seq.Name, err)
		}
	}

	for _, tbl := range meta.Tables {
		if _, err := tx.Exec(ctx, "ALTER TABLE "+pgx.Identifier{tbl.Schema, tbl.Name}.Sanitize()+" ENABLE TRIGGER USER"); err != nil {
			return 0, fmt.Errorf("backup: enable triggers on %s.%s: %w", tbl.Schema, tbl.Name, err)
		}
	}

	var headPos int64
	if verifyAudit {
		res, err := audit.VerifyTx(ctx, tx, 0, 0)
		if err != nil {
			return 0, fmt.Errorf("backup: verify audit chain: %w", err)
		}
		if !res.OK {
			return 0, fmt.Errorf("backup: audit chain verification failed at position %d (%s)", res.Position, res.Reason)
		}
		if err := tx.QueryRow(ctx, `SELECT position FROM audit.head WHERE one`).Scan(&headPos); err != nil {
			return 0, fmt.Errorf("backup: read audit head: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("backup: commit restore transaction: %w", err)
	}
	return headPos, nil
}

func validatedPartitionBound(bound string) string {
	if bound == "DEFAULT" {
		return "DEFAULT"
	}
	m := partBoundRangeRE.FindStringSubmatch(bound)
	if len(m) != 3 {
		return ""
	}
	return fmt.Sprintf("FOR VALUES FROM ('%s') TO ('%s')", m[1], m[2])
}

func enterReplicaOrDeferredMode(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT upmo_backup_repl"); err != nil {
		return fmt.Errorf("backup: savepoint: %w", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = 'replica'"); err != nil {
		if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT upmo_backup_repl"); rbErr != nil {
			return fmt.Errorf("backup: rollback savepoint: %w", rbErr)
		}
		if _, defErr := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); defErr != nil {
			return fmt.Errorf("backup: defer constraints: %w", defErr)
		}
		return nil
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT upmo_backup_repl"); err != nil {
		return fmt.Errorf("backup: release savepoint: %w", err)
	}
	return nil
}

func copyTablesFromTarTx(ctx context.Context, tx pgx.Tx, tables []TableMeta, payloadTarPath string, tableSizes map[string]int64) error {
	need := make(map[string]TableMeta, len(tables))
	for _, tbl := range tables {
		entryPath := fmt.Sprintf("%s%s.%s.copy", dbTablesPrefix, tbl.Schema, tbl.Name)
		if tableSizes[entryPath] > 0 {
			need[entryPath] = tbl
		}
	}
	if len(need) == 0 {
		return nil
	}

	f, err := os.Open(payloadTarPath) //nolint:gosec // temp file created by Restore
	if err != nil {
		return fmt.Errorf("backup: open payload archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("backup: open payload gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("backup: read payload tar: %w", err)
		}
		clean, err := cleanArchivePath(hdr.Name)
		if err != nil {
			return err
		}
		tbl, ok := need[clean]
		if !ok {
			continue
		}
		cols := make([]string, len(tbl.Columns))
		for i, c := range tbl.Columns {
			cols[i] = pgx.Identifier{c}.Sanitize()
		}
		sql := "COPY " + pgx.Identifier{tbl.Schema, tbl.Name}.Sanitize() + " (" + strings.Join(cols, ", ") + ") FROM STDIN (FORMAT text)"
		if _, err := tx.Conn().PgConn().CopyFrom(ctx, tr, sql); err != nil {
			return fmt.Errorf("backup: copy into %s.%s: %w", tbl.Schema, tbl.Name, err)
		}
		delete(need, clean)
		if len(need) == 0 {
			return nil
		}
	}
	for entryPath := range need {
		return fmt.Errorf("backup: table data %s missing from archive", entryPath)
	}
	return nil
}

func topoSortTables(tables []TableMeta, fks []ForeignKeyMeta) []TableMeta {
	key := func(schema, name string) string { return schema + "." + name }
	byKey := make(map[string]TableMeta, len(tables))
	inDegree := make(map[string]int, len(tables))
	dependents := make(map[string][]string, len(tables))
	for _, t := range tables {
		k := key(t.Schema, t.Name)
		byKey[k] = t
		inDegree[k] = 0
	}
	seenEdge := make(map[string]bool, len(fks))
	for _, fk := range fks {
		from := key(fk.FromSchema, fk.FromTable)
		to := key(fk.ToSchema, fk.ToTable)
		if from == to {
			continue
		}
		if _, ok := byKey[from]; !ok {
			continue
		}
		if _, ok := byKey[to]; !ok {
			continue
		}
		edge := to + "->" + from
		if seenEdge[edge] {
			continue
		}
		seenEdge[edge] = true
		dependents[to] = append(dependents[to], from)
		inDegree[from]++
	}

	var queue []string
	for k, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, k)
		}
	}
	sort.Strings(queue)

	var out []TableMeta
	placed := make(map[string]bool, len(tables))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		out = append(out, byKey[cur])
		placed[cur] = true
		for _, dep := range dependents[cur] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
				sort.Strings(queue)
			}
		}
	}
	if len(out) < len(tables) {
		var rem []string
		for _, t := range tables {
			k := key(t.Schema, t.Name)
			if !placed[k] {
				rem = append(rem, k)
			}
		}
		sort.Strings(rem)
		for _, k := range rem {
			out = append(out, byKey[k])
		}
	}
	return out
}
