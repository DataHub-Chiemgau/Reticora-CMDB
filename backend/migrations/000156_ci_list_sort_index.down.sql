-- Reverts WP-605: the CI list without an index of its default sort.

DROP INDEX idx_ci_list_sort;
