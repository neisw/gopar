#!/bin/bash
# E2E test launcher for gopar.
# Starts a PostgreSQL container, creates an isolated test database,
# runs the e2e test suite, then tears everything down.
#
# Usage:
#   ./scripts/e2e.sh          # from repo root
#   make e2e                  # via Makefile

set -euo pipefail

DOCKER="${DOCKER:-podman}"
PSQL_CONTAINER="gopar-e2e-postgresql"
PSQL_PORT="23433"
E2E_DB_NAME="gopar_e2e"
E2E_EXIT_CODE=0

# ---------------------------------------------------------------------------
# Safety helpers — refuse to operate on non-e2e or non-local databases
# ---------------------------------------------------------------------------
e2e_safe_db_name() {
    local db_name="$1"
    case "$db_name" in
        *_e2e*) ;;
        *) echo "FATAL: refusing to operate on database '$db_name' — name must contain '_e2e'" >&2; exit 1 ;;
    esac
}

e2e_safe_dsn_host() {
    local dsn="$1"
    case "$dsn" in
        *@localhost:*|*@localhost/*|*@127.0.0.1:*|*@127.0.0.1/*) ;;
        *) echo "FATAL: refusing to operate on non-local database: $dsn" >&2; exit 1 ;;
    esac
}

e2e_create_database() {
    local db_name="$1"
    local admin_dsn="$2"
    e2e_safe_db_name "$db_name"
    e2e_safe_dsn_host "$admin_dsn"
    echo "Dropping e2e database if it exists: $db_name"
    psql "$admin_dsn" -c "DROP DATABASE IF EXISTS $db_name" 2>/dev/null || true
    echo "Creating e2e database: $db_name"
    psql "$admin_dsn" -c "CREATE DATABASE $db_name"
}

e2e_drop_database() {
    local db_name="$1"
    local admin_dsn="$2"
    e2e_safe_db_name "$db_name"
    e2e_safe_dsn_host "$admin_dsn"
    echo "Dropping e2e database: $db_name"
    psql "$admin_dsn" -c "DROP DATABASE IF EXISTS $db_name" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# Devcontainer vs host setup
# ---------------------------------------------------------------------------
if [ -f /run/.containerenv ]; then
    echo "Detected devcontainer — using existing PostgreSQL service"
    ADMIN_DSN="${GOPAR_ADMIN_DSN:-postgresql://postgres:password@localhost:5432/postgres}"
    E2E_DSN=$(echo "$ADMIN_DSN" | sed "s|/[^/]*$|/${E2E_DB_NAME}|")

    e2e_create_database "$E2E_DB_NAME" "$ADMIN_DSN"

    export GOPAR_TEST_DSN="$E2E_DSN"
    echo "E2E DSN: $GOPAR_TEST_DSN"

    clean_up() {
        ARG=$?
        if [ $ARG -ne 0 ]; then
            E2E_EXIT_CODE=$ARG
        fi
        e2e_drop_database "$E2E_DB_NAME" "$ADMIN_DSN"
        exit $E2E_EXIT_CODE
    }
    trap clean_up EXIT
else
    clean_up() {
        ARG=$?
        if [ $ARG -ne 0 ]; then
            E2E_EXIT_CODE=$ARG
        fi
        echo "Tearing down container $PSQL_CONTAINER"
        $DOCKER stop $PSQL_CONTAINER 2>/dev/null || true
        $DOCKER rm $PSQL_CONTAINER 2>/dev/null || true
        exit $E2E_EXIT_CODE
    }
    trap clean_up EXIT

    # Clean up any leftover container from a previous run
    echo "Cleaning up old gopar e2e container if present"
    $DOCKER stop $PSQL_CONTAINER 2>/dev/null || true
    $DOCKER rm $PSQL_CONTAINER 2>/dev/null || true

    echo "Starting PostgreSQL container: $PSQL_CONTAINER (port $PSQL_PORT)"
    $DOCKER run --name $PSQL_CONTAINER \
        -e POSTGRES_PASSWORD=password \
        -e POSTGRES_HOST_AUTH_METHOD=trust \
        -p 127.0.0.1:$PSQL_PORT:5432 \
        -d quay.io/enterprisedb/postgresql

    echo "Waiting for PostgreSQL to be ready..."
    TIMEOUT=30
    ELAPSED=0
    while [ $ELAPSED -lt $TIMEOUT ]; do
        if $DOCKER exec $PSQL_CONTAINER pg_isready -U postgres > /dev/null 2>&1; then
            echo "PostgreSQL is ready after ${ELAPSED}s"
            break
        fi
        sleep 1
        ELAPSED=$((ELAPSED + 1))
    done

    if [ $ELAPSED -ge $TIMEOUT ]; then
        echo "FATAL: PostgreSQL did not start within ${TIMEOUT}s"
        exit 1
    fi

    ADMIN_DSN="postgresql://postgres:password@localhost:$PSQL_PORT/postgres"
    e2e_create_database "$E2E_DB_NAME" "$ADMIN_DSN"

    export GOPAR_TEST_DSN="postgresql://postgres:password@localhost:$PSQL_PORT/$E2E_DB_NAME?sslmode=disable"
    echo "E2E DSN: $GOPAR_TEST_DSN"
fi

# ---------------------------------------------------------------------------
# Run e2e tests
# ---------------------------------------------------------------------------
echo ""
echo "Running gopar e2e tests..."
go test -v -timeout 5m -count 1 ./test/e2e/...
E2E_EXIT_CODE=$?
