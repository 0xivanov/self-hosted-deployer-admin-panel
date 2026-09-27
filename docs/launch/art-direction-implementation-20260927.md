# Art direction implementation, 27 September 2026

Applied the local `docs/design/2026-09-27/ART_DIRECTION.md` direction to both running interfaces.

- Shared readable type scale, 44px controls, spacing, borders and keyboard focus.
- Customer portal: light workspace rail; website Overview, Statistics and existing operational sections; desktop section buttons and a mobile selector. Workspace features retain existing permission/capability checks. Dark theme remains supported.
- Operator console: dark navigation rail; Overview, Statistics, Deployments, Runtime & logs and Configuration sections. Environment/session context remains visible. Observation copy explicitly describes retrieval time rather than measurement time. Configuration restore scope is explicit.
- Statistics displays unavailable monitoring rather than synthetic chart values. Actual traffic collection, historical availability and chart APIs remain future work. The review's invented identities/metrics are not shipped.
- Fixed overview relocation during asynchronous file refresh, upload navigation to the Files section, and the missing portfolio-tools ID used to restore the creation form.

Validation: 62 frontend tests; admin UI/client Go tests; both Linux builds. Local disposable demos verified sign-in, website creation, overview/section switching, unavailable statistics and admin configuration isolation. Portal viewport checks at 320, 390 and 1440 CSS pixels had no document horizontal overflow; light and dark surfaces visually inspected. This is not a complete accessibility audit or the implementation of a telemetry backend.

Existing publishing, billing, domain purchase and infrastructure APIs remain unchanged. No database migration or worker update is required.
