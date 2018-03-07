#!/bin/bash

# eb need to set: RUN_IN_EB, S3_BUCKET, SYNC_TO_S3 (boolean) if a syncer
# hive expects: DATABASE_URL, LOG_LEVEL, STEEMD_URL, JUSSI_URL
# default DATABASE_URL should be postgresql://postgres:postgres@localhost:5432/postgres

POPULATE_CMD="$(which hive)"

if [[ "$RUN_IN_EB" ]]; then
  # mkdir /var/lib/postgresql/9.5
  mkdir /var/lib/postgresql/9.5/main
  chown -R postgres:postgres /var/lib/postgresql/9.5
  cd /var/lib/postgresql/9.5
  echo hivemind: attempting to pull in state file from s3://$S3_BUCKET/hivemind-$SCHEMA_HASH-latest.tar.bz2
  s3cmd get s3://$S3_BUCKET/hivemind-$SCHEMA_HASH-latest.tar.bz2 - | pbzip2 -m2000dc | tar x
  if [[ $? -ne 0 ]]; then
    if [[ ! "$SYNC_TO_S3" ]]; then
      echo notifyalert hivemind: unable to pull state from S3 - exiting
      exit 1
    else
      echo hivemindsync: state file for schema version $SCHEMA_HASH not found, creating a new one from genesis
      # initialize a new postgres db to start fresh
      chpst -upostgres /usr/lib/postgresql/9.5/bin/initdb -D /var/lib/postgresql/9.5/main
    fi
  fi
  service postgresql start
  sudo -u postgres psql --command '\password postgres'
fi

cd $APP_ROOT

# startup hive
exec "${POPULATE_CMD}" sync 2>&1&

if [[ ! "$SYNC_TO_S3" ]]; then
	exec "${POPULATE_CMD}" server
else
	exec "${POPULATE_CMD}" server 2>&1&
    mkdir -p /etc/service/hivesync
    cp /usr/local/bin/hivesync.sh /etc/service/hivesync/run
    chmod +x /etc/service/hivesync/run
    runsv /etc/service/hivesync
fi

echo hivemind: application has stopped, see log for errors