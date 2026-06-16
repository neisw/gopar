package gopar

import (
	"database/sql"

	"github.com/openshift-eng/gopar/partitioning"
)

// DB wraps a database/sql connection and provides PostgreSQL partition management
// utilities. This library focuses on partition lifecycle management
// and migrating data from existing tables to partitioned tables.
//
// It does NOT include schema creation/update functionality - partitioned tables
// should be created using standard DDL before using this library.
type DB struct {
	DB *sql.DB
}

// New creates a new gopar DB instance wrapping the provided database connection.
// The provided *sql.DB should already be connected to a PostgreSQL database.
//
// Example:
//
//	import (
//	    "database/sql"
//	    _ "github.com/lib/pq"
//	    "github.com/openshift-eng/gopar"
//	)
//
//	// Connect to PostgreSQL
//	dsn := "host=localhost user=postgres password=postgres dbname=mydb port=5432 sslmode=disable"
//	db, err := sql.Open("postgres", dsn)
//	if err != nil {
//	    panic(err)
//	}
//
//	// Wrap with gopar
//	g := gopar.New(db)
//
//	// Use gopar's partition management features
//	partitions, err := g.Partitions().ListPartitionedTables()
func New(db *sql.DB) *DB {
	return &DB{DB: db}
}

// NewPartitions creates a DB_PARTITIONS instance for partition management operations
func NewPartitions(db *sql.DB) *partitioning.DB_PARTITIONS {
	return partitioning.NewPartitions(db)
}
