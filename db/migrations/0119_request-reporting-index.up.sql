-- Unmarked legacy requests cannot enter an experiment report. Keep discovery
-- and selected-cohort reads on the smaller request-time provenance population.
CREATE INDEX CONCURRENTLY model_router_request_reporting_time_idx
    ON router.model_router_request_telemetry (installation_id, timestamp DESC)
    WHERE cohort_experiment_id IS NOT NULL
       OR (reporting_schema_version = 1
           AND reporting_mode IN ('percentage', 'teams')
           AND reporting_experiment_id IS NOT NULL
           AND reporting_revision > 0);
