package gopar

import (
	"gorm.io/gorm"

	"github.com/neisw/gopar/partitioning"
)

// DB wraps a GORM database connection and provides PostgreSQL partition management
// and migration utilities. This library focuses on partition lifecycle management
// and migrating data from existing tables to partitioned tables.
//
// It does NOT include schema creation/update functionality - partitioned tables
// should be created using standard DDL or GORM AutoMigrate before using this library.
type DB struct {
	DB *gorm.DB
}

// New creates a new gopar DB instance wrapping the provided GORM database connection.
// The provided *gorm.DB should already be connected to a PostgreSQL database.
//
// Example:
//
//	import (
//	    "gorm.io/driver/postgres"
//	    "gorm.io/gorm"
//	    "github.com/neisw/gopar"
//	)
//
//	// Connect to PostgreSQL using GORM
//	dsn := "host=localhost user=postgres password=postgres dbname=mydb port=5432 sslmode=disable"
//	gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
//	if err != nil {
//	    panic(err)
//	}
//
//	// Wrap with gopar
//	db := gopar.New(gormDB)
//
//	// Use gopar's partition management features
//	partitions, err := db.Partitions().ListPartitionedTables()
func New(gormDB *gorm.DB) *DB {
	return &DB{DB: gormDB}
}

// Partitions returns a DB_PARTITIONS instance for partition management operations
func (db *DB) Partitions() *partitioning.DB_PARTITIONS {
	return &partitioning.DB_PARTITIONS{DB: db.DB}
}
