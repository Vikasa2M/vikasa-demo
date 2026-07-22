# Ask-the-data questions (frontier model, same MCP session)

Preface (say once): "Now answer some operational questions. For every
answer, show the SQL you ran and cite the numbers it returned."

Q1. "MARDOT cabinet cab-i85-001 lost its network connection earlier today — did we
     lose any data? Prove it."
     Correct answer must: query event counts/heartbeat continuity across the outage
     window, check for duplicate ce_id values, and conclude zero loss + zero
     duplicates (the buffered backfill). This is the key result — the AI
     independently re-verifies the demo's core claim.

Q2. "Which intersection had the most MAX_OUT phase terminations in the last
     3 hours, and what does that suggest about its signal timing?"
     Must: aggregate phase_state_change where to_state yellow / termination_reason
     MAX_OUT grouped by cabinet, name the worst, and explain max-out ≈ demand
     exceeding the split (split-failure pressure).

Q3. "Is I-85 slower right now than it was an hour ago? Per DOT."
     Must: use vikasa_federation lane-interval speeds, compare two windows,
     give km/h deltas per DOT.

Q4. "Are the camera and the lidar at veldot cab-i85-101 agreeing with each other?"
     Must: compare traffic_sensor_lane_interval volumes vs perception_zone_interval
     counts over the same window and state the agreement ratio.

Q5. "Which DMS signs are currently showing advisories, and can you correlate that
     with anything else happening on the network?"
     Must: latest mode per sign from dms_event, then join the reasoning to active
     perception incidents on the same corridor.
