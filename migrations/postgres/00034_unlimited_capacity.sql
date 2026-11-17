-- +goose Up
-- Zero means unlimited for one budget dimension; preserve configured budgets.
ALTER TABLE capacity_budgets
 DROP CONSTRAINT capacity_budgets_cpu_limit_check,
 DROP CONSTRAINT capacity_budgets_memory_mib_limit_check,
 DROP CONSTRAINT capacity_budgets_disk_mib_limit_check,
 ADD CONSTRAINT capacity_budgets_cpu_limit_check CHECK (cpu_limit >= 0),
 ADD CONSTRAINT capacity_budgets_memory_mib_limit_check CHECK (memory_mib_limit >= 0),
 ADD CONSTRAINT capacity_budgets_disk_mib_limit_check CHECK (disk_mib_limit >= 0);
