Attached is the data-model documentation for our Vikasa deployment (three state
DOTs sharing an I-85 corridor). Using the vikasa_federation database — which contains only the
corridor data each DOT chose to share through its DMZ — build a dashboard called
"Corridor Operations — AI Built" showing:

1. Average corridor speed over time, one series per DOT.
2. A map of the corridor cabinets colored by their current average speed.
3. A table of active incidents (most recent state per incident).
4. Current DMS advisory status per sign.
5. Corridor event throughput (events/sec) by DOT.

Explore the data first. Verify every query returns rows before adding its panel.
