/* 2026-10 生产:Web 命令行 \i 执行这份脚本,第 11 条(第一个 CREATE FUNCTION)被拆成 13 段,
   第一段报 1064。原样保留结构(DELIMITER $$、函数 / 过程体里的分号、动态 SQL),供分句回归用。 */

SET @TARGET_DATE   = '2027-12-31';
SET @DRY_RUN       = 1;
SET @INCLUDE_MAYBE = 0;

DROP TEMPORARY TABLE IF EXISTS tmp_partition_log_20261014;
CREATE TEMPORARY TABLE tmp_partition_log_20261014 (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    table_schema VARCHAR(64),
    table_name   VARCHAR(64),
    status       VARCHAR(12),   -- ADDED / DRY RUN / SKIPPED / COVERED / ERROR
    details      TEXT,
    stmt         LONGTEXT
);

DROP PROCEDURE IF EXISTS tmp_pt_add_until_20261014;
DROP PROCEDURE IF EXISTS tmp_pt_log_20261014;
DROP FUNCTION  IF EXISTS tmp_pt_kind_20261014;
DROP FUNCTION  IF EXISTS tmp_pt_to_date_20261014;
DROP FUNCTION  IF EXISTS tmp_pt_to_literal_20261014;

DELIMITER $$

/* Classify the partitioning scheme from expression + last bound */
CREATE FUNCTION tmp_pt_kind_20261014(p_expr TEXT, p_desc TEXT)
RETURNS VARCHAR(10) NO SQL
BEGIN
    DECLARE v_e TEXT;
    SET v_e = UPPER(IFNULL(p_expr, ''));

    IF p_desc REGEXP '^''[0-9]{4}-[0-9]{2}-[0-9]{2}' THEN
        RETURN 'DATE';
    ELSEIF p_desc REGEXP '^[0-9]+$' THEN
        IF v_e LIKE '%TO_DAYS(%' THEN
            RETURN 'TODAYS';
        ELSEIF v_e LIKE '%UNIX_TIMESTAMP(%' THEN
            RETURN 'UNIXTS';
        ELSEIF v_e NOT LIKE '%(%' THEN
            IF CHAR_LENGTH(p_desc) = 8 AND LEFT(p_desc, 2) IN ('19','20') THEN
                RETURN 'YMD';
            END IF;
        END IF;
    END IF;
    RETURN 'UNKNOWN';
END$$

/* Partition upper-bound description -> DATE */
CREATE FUNCTION tmp_pt_to_date_20261014(p_kind VARCHAR(10), p_desc TEXT)
RETURNS DATE NO SQL
BEGIN
    RETURN CASE p_kind
        WHEN 'DATE'   THEN STR_TO_DATE(SUBSTRING(REPLACE(p_desc, '''', ''), 1, 10), '%Y-%m-%d')
        WHEN 'TODAYS' THEN FROM_DAYS(CAST(p_desc AS UNSIGNED))
    END;
END$$

/* DATE -> literal usable inside VALUES LESS THAN (...) */
CREATE FUNCTION tmp_pt_to_literal_20261014(p_kind VARCHAR(10), p_date DATE, p_has_time TINYINT)
RETURNS VARCHAR(40) NO SQL
BEGIN
    RETURN CASE p_kind
        WHEN 'DATE'   THEN CONCAT('''', DATE_FORMAT(p_date, '%Y-%m-%d'),
                                  IF(p_has_time = 1, ' 00:00:00', ''), '''')
        WHEN 'TODAYS' THEN CAST(TO_DAYS(p_date) AS CHAR)
    END;
END$$

CREATE PROCEDURE tmp_pt_log_20261014(p_schema VARCHAR(64), p_table VARCHAR(64),
                            p_status VARCHAR(12), p_details TEXT, p_stmt LONGTEXT)
BEGIN
    INSERT INTO tmp_partition_log_20261014 (table_schema, table_name, status, details, stmt)
    VALUES (p_schema, p_table, p_status, p_details, p_stmt);
END$$

CREATE PROCEDURE tmp_pt_add_until_20261014(IN p_target DATE,
                                  IN p_dry_run TINYINT,
                                  IN p_include_maybe TINYINT)
BEGIN
    DECLARE v_schema   VARCHAR(64);
    DECLARE v_table    VARCHAR(64);
    DECLARE v_sql      LONGTEXT;
    DECLARE v_status   VARCHAR(12);
    DECLARE v_msg      TEXT;
    DECLARE v_done     INT DEFAULT 0;

    DECLARE cur CURSOR FOR
        SELECT p.table_schema, p.table_name
        FROM   information_schema.partitions p
        WHERE  p.partition_method LIKE 'RANGE%'
        ORDER  BY p.table_schema, p.table_name;

    DECLARE CONTINUE HANDLER FOR NOT FOUND SET v_done = 1;

    OPEN cur;

    read_loop: LOOP
        FETCH cur INTO v_schema, v_table;
        IF v_done = 1 THEN
            LEAVE read_loop;
        END IF;

        SET v_sql = CONCAT('ALTER TABLE `', REPLACE(v_schema, '`', '``'), '`.`',
                           REPLACE(v_table, '`', '``'), '` ADD PARTITION (', CHAR(10), ')');

        IF p_dry_run = 1 THEN
            SET v_status = 'DRY RUN';
        ELSE
            BEGIN
                DECLARE EXIT HANDLER FOR SQLEXCEPTION
                BEGIN
                    GET DIAGNOSTICS CONDITION 1 v_msg = MESSAGE_TEXT;
                    SET v_status = 'ERROR';
                END;

                SET @tmp_pt_sql = v_sql;
                PREPARE tmp_pt_stmt FROM @tmp_pt_sql;
                EXECUTE tmp_pt_stmt;
                DEALLOCATE PREPARE tmp_pt_stmt;
                SET v_status = 'ADDED';
            END;
        END IF;

        CALL tmp_pt_log_20261014(v_schema, v_table, v_status, v_msg, v_sql);
    END LOOP;

    CLOSE cur;
END$$

DELIMITER ;

CALL tmp_pt_add_until_20261014(CAST(@TARGET_DATE AS DATE), @DRY_RUN, @INCLUDE_MAYBE);

SELECT status, COUNT(*) AS tables
FROM   tmp_partition_log_20261014
GROUP  BY status
ORDER  BY FIELD(status, 'ADDED', 'DRY RUN', 'ERROR', 'COVERED', 'SKIPPED');

SELECT table_schema, table_name, status, details, stmt
FROM   tmp_partition_log_20261014
ORDER  BY FIELD(status, 'ERROR', 'ADDED', 'DRY RUN', 'SKIPPED', 'COVERED'),
          table_schema, table_name;

DROP PROCEDURE IF EXISTS tmp_pt_add_until_20261014;
DROP PROCEDURE IF EXISTS tmp_pt_log_20261014;
DROP FUNCTION  IF EXISTS tmp_pt_kind_20261014;
DROP FUNCTION  IF EXISTS tmp_pt_to_date_20261014;
DROP FUNCTION  IF EXISTS tmp_pt_to_literal_20261014;
