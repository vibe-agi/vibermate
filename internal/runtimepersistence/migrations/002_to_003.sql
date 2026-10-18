ALTER TABLE provider_accounts ADD COLUMN cyber_access_program TEXT NOT NULL DEFAULT 'standard' CHECK(cyber_access_program IN ('standard','daybreak_blue','daybreak_red'));
