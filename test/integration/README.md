# Local MariaDB integration test

This environment is disposable. It uses Docker only for development testing;
the FiveMesh Backup binary has no Docker runtime dependency.

From the repository root:

```sh
bash test/integration/run.sh
```

The fixture creates a database with InnoDB and MyISAM tables, a view, procedure,
function, trigger, event, and 50,000 generated rows. The script performs a real
native dump, verifies the compressed archive, restores it into a separate
database, and compares row counts.

Never point these tests at a production database.

