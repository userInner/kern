Design and implement a polished responsive single-page web application named **POLARIS**, an operations console for the `AURORA-7` polar research expedition.

Work only in `index.html`, `styles.css`, and `app.js`. Do not modify tests or configuration. Use plain HTML, CSS, and JavaScript with no external packages, fonts, images, CDNs, or network requests. Everything must work by opening `index.html` locally. Do not use or invoke any Skills, plugins, repository-specific instructions, or design templates.

## Visual direction

Create a restrained, precise field-instrument aesthetic: polar-night surfaces with ice blue and signal yellow accents. Avoid generic admin-dashboard styling, excessive gradients, large-area glass blur, and decorative effects that reduce scanability. Icons must be inline SVG or CSS.

At `1440×1000`, use an asymmetric information-rich layout with a visible left sidebar. At `390×844`, replace the sidebar with fixed bottom navigation, preserve the radar and timeline, use a single-column flow with a two-column environmental-metrics grid, leave room above the bottom navigation, and prevent all horizontal overflow.

## Fixed product content

- Brand: `POLARIS`, caption `FIELD OPERATIONS`.
- Mission: `AURORA-7`; date `18 NOV 2026`; local time `14:32 UTC−3`; status `LIVE · 42ms`.
- Hero eyebrow: `EXPEDITION 04 / DAY 18`.
- Hero title: `Reading the ice before it moves.`
- Hero summary: `AURORA-7 is mapping subglacial movement across the Sørsdal corridor before tonight’s pressure front arrives.`
- Progress: `68%`; remaining: `11 days remaining`; primary action: `Send field update`.
- Sidebar destinations: `Mission overview`, `Science data`, `Logistics`, `Comms archive`.
- Top views: `Overview`, `Science`, `Logistics`.

Environmental metrics:

- `Surface temp` — `−28.4°C` — `↓ 2.1°`
- `Wind speed` — `38 km/h` — `↑ 6 km/h`
- `Visibility` — `4.8 km` — `Stable`
- `Ice drift` — `12 cm/h` — `↑ 3 cm/h`

Build a pure-CSS or inline-SVG polar scan/radar visualization and label these observation points:

- `P-01` — `Sørsdal Gate` — `Active` — `68°36′S`
- `P-02` — `Blue Rift` — `Sampling` — `78°18′E`
- `P-03` — `Echo Ridge` — `Standby` — `68°41′S`

Today's action timeline:

- `07:40` — `Field` — `Sensor line deployed` — `12 nodes · Sector C`
- `09:15` — `Lab` — `Core sample received` — `Sample AR7-184`
- `11:30` — `Field` — `P-02 calibration` — `Signal variance 1.8%`
- `13:05` — `Comms` — `Orbital uplink complete` — `2.4 GB transferred`
- `16:20` — `Lab` — `Salinity analysis` — `Scheduled`

Crew:

- `Mara Voss` — `Expedition Lead` — `On field`
- `Eli Navarro` — `Glaciologist` — `In lab`
- `Jun Park` — `Systems Engineer` — `At P-02`

Initial comms log:

- `14:28` — `Jun` — `P-02 calibration is holding below 2% variance.`
- `13:47` — `Mara` — `Returning through the east marker route.`
- `13:05` — `System` — `Orbital uplink completed successfully.`

Weather advisory:

- Label: `WEATHER ADVISORY 02`
- Title: `Pressure front approaching`
- Body: `Wind may exceed 70 km/h after 20:00. Outdoor operations should conclude by 18:30.`
- Initial action: `Acknowledge`

## Required interactions

1. View switching: mark the active `Overview / Science / Logistics` control with `aria-selected`. Use `data-view` controls and `#view-summary`. Science shows `3 observation points are returning usable ice-motion data.` Logistics shows `8 of 11 field assets are ready for the evening shutdown.`
2. Timeline filtering: `All / Field / Lab` controls use `data-filter`; entries expose `data-type`. Field shows only 07:40 and 11:30. Lab shows only 09:15 and 16:20. All restores five entries.
3. Advisory acknowledgement: the action becomes `Acknowledged by operator`, the card gains a resolved visual state, and exactly one new top log entry appears: `14:32 — Operator — Weather advisory acknowledged.` Repeated activation must not duplicate it.
4. Field-update dialog: `Send field update` opens a modal titled `New field update`. Include a textarea with placeholder `Share an observation with the expedition team…`, live character count capped at 160, disabled submit for empty input, and initial focus in the textarea. Submitting `P-03 battery pack replaced.` closes the dialog and adds that message to the top of the comms log. Escape, close button, and backdrop click close the dialog.
5. Accessibility: semantic landmarks, visible keyboard focus, readable contrast, sensible ARIA, an `aria-live` feedback region, and reduced-motion handling.

Run `node --test` and `node --check app.js` before finishing.
