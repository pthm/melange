#!/usr/bin/env bash
#
# Start a PostgreSQL container for integration testing.
#
# This script starts a PostgreSQL container and prints the DATABASE_URL.
# Use this for manual testing or CI environments where you want explicit
# control over the database lifecycle.
#
# Usage:
#   export DATABASE_URL=$(./scripts/start-test-db.sh)
#   just test-integration
#   just test-ts
#   docker stop melange-test-db

set -euo pipefail

CONTAINER_NAME="melange-test-db"
POSTGRES_VERSION="18-alpine"
POSTGRES_USER="test"
POSTGRES_PASSWORD="test"
POSTGRES_DB="postgres"
CONTAINER_PORT="5432"

# Check if container already exists
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    # Container exists - check if it's running
    if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
        # Already running
        HOST_PORT=$(docker port ${CONTAINER_NAME} 5432 | cut -d: -f2)
        echo "postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:${HOST_PORT}/${POSTGRES_DB}?sslmode=disable"
        exit 0
    else
        # Start existing container
        docker start ${CONTAINER_NAME} >/dev/null
        sleep 2
        HOST_PORT=$(docker port ${CONTAINER_NAME} 5432 | cut -d: -f2)
        echo "postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:${HOST_PORT}/${POSTGRES_DB}?sslmode=disable"
        exit 0
    fi
fi

# Start new container
docker run -d \
    --name ${CONTAINER_NAME} \
    -e POSTGRES_USER=${POSTGRES_USER} \
    -e POSTGRES_PASSWORD=${POSTGRES_PASSWORD} \
    -e POSTGRES_DB=${POSTGRES_DB} \
    -p ${CONTAINER_PORT} \
    postgres:${POSTGRES_VERSION} >/dev/null

# Wait for PostgreSQL to be ready
sleep 2

for i in {1..30}; do
    if docker exec ${CONTAINER_NAME} pg_isready -U ${POSTGRES_USER} >/dev/null 2>&1; then
        break
    fi
    if [ $i -eq 30 ]; then
        echo "ERROR: PostgreSQL failed to start" >&2
        docker logs ${CONTAINER_NAME} >&2
        docker stop ${CONTAINER_NAME} >/dev/null
        docker rm ${CONTAINER_NAME} >/dev/null
        exit 1
    fi
    sleep 1
done

# Get the mapped port
HOST_PORT=$(docker port ${CONTAINER_NAME} 5432 | cut -d: -f2)

# Print DATABASE_URL
echo "postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:${HOST_PORT}/${POSTGRES_DB}?sslmode=disable"
