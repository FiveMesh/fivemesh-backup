CREATE DATABASE IF NOT EXISTS fxbackup_test CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS fxbackup_empty CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS fxbackup_restore CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE USER IF NOT EXISTS 'backup_test'@'%' IDENTIFIED BY 'backup-test-password';
GRANT SELECT, SHOW VIEW, TRIGGER, LOCK TABLES, EVENT, EXECUTE
  ON fxbackup_test.* TO 'backup_test'@'%';
GRANT SELECT, SHOW VIEW, TRIGGER, LOCK TABLES, EVENT, EXECUTE
  ON fxbackup_empty.* TO 'backup_test'@'%';
GRANT ALL PRIVILEGES ON fxbackup_restore.* TO 'backup_test'@'%';
GRANT SHOW CREATE ROUTINE ON *.* TO 'backup_test'@'%';
GRANT SET USER ON *.* TO 'backup_test'@'%';

CREATE USER IF NOT EXISTS 'backup_limited'@'%' IDENTIFIED BY 'limited-test-password';
GRANT SELECT, SHOW VIEW, TRIGGER, LOCK TABLES, EVENT, EXECUTE
  ON fxbackup_test.* TO 'backup_limited'@'%';

USE fxbackup_test;

CREATE TABLE players (
  id BIGINT NOT NULL AUTO_INCREMENT,
  license VARCHAR(64) NOT NULL,
  display_name VARCHAR(100) NOT NULL,
  metadata JSON NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  UNIQUE KEY uq_players_license (license)
) ENGINE=InnoDB;

CREATE TABLE legacy_counters (
  counter_name VARCHAR(64) NOT NULL,
  counter_value BIGINT NOT NULL,
  PRIMARY KEY (counter_name)
) ENGINE=MyISAM;

INSERT INTO legacy_counters VALUES ('server_starts', 42);
INSERT INTO players (license, display_name, metadata)
SELECT
  CONCAT('license-', seq),
  CONCAT('Player ', seq),
  JSON_OBJECT('seed', seq)
FROM seq_1_to_50000;

CREATE VIEW active_players AS
SELECT id, license, display_name
FROM players
WHERE display_name IS NOT NULL;

DELIMITER //
CREATE PROCEDURE player_count(OUT total BIGINT)
BEGIN
  SELECT COUNT(*) INTO total FROM players;
END//

CREATE FUNCTION player_label(player_id BIGINT)
RETURNS VARCHAR(120)
DETERMINISTIC
READS SQL DATA
BEGIN
  DECLARE result VARCHAR(120);
  SELECT display_name INTO result FROM players WHERE id = player_id;
  RETURN result;
END//

CREATE TRIGGER normalize_display_name
BEFORE INSERT ON players
FOR EACH ROW
SET NEW.display_name = TRIM(NEW.display_name)//
DELIMITER ;

CREATE EVENT purge_old_players
ON SCHEDULE EVERY 1 DAY
STARTS CURRENT_TIMESTAMP + INTERVAL 1 DAY
DO DELETE FROM players WHERE created_at < CURRENT_TIMESTAMP - INTERVAL 30 DAY;

