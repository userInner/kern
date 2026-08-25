# Frontend Blind Design Evaluation

This evaluation compares two unassisted general agents on the same frontend brief and immutable starter project. Kern runs as `general.base` without plugins. The Codex adapter ignores user configuration and repository rules.

## Prerequisite checks

- All three production files must be changed, and tests/configuration must remain unchanged.
- JavaScript must parse and the local interaction contract must pass.
- The page must use semantic landmarks, provide a dialog, include responsive and reduced-motion rules, and avoid network-hosted assets.

These checks only establish that an output is eligible for visual review. They do not decide design quality.

## Anonymous human rubric — 100 points

- Visual hierarchy and scanability: 25
- Aesthetic coherence and craft: 20
- Information architecture: 15
- Interaction clarity and state feedback: 15
- Desktop/mobile responsiveness: 15
- Accessibility and readability: 10

Desktop and mobile screenshots are presented as anonymous A/B pairs. Reviewers should score the images before the agent mapping is revealed.
