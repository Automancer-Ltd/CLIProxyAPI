# t16638: v8.0.60 integration investigation

Fork head: 1421b638. Common upstream ancestor: f7331b5197730226829cab32c31fb4ce546ef8fc. Tag v8.0.60 has 111 commits after that ancestor. The fork already includes changes before that ancestor, including the v8.0.45 descriptor exhaustion and zstd bounds fixes; they are not outstanding in this comparison.

Security-relevant candidates, preliminary severity assessments (not upstream CVSS ratings):

- High, conditional on untrusted or separately trusted plugins: 3698b159 scopes model streams to the owning plugin and callback instance, preventing cross-plugin or cross-instance stream reads and closes. d96b1941 closes a concurrent teardown registration race in the same ownership boundary.
- Medium, conditional on use of disabled plugin credentials: 04208efa preserves disabled state during plugin refresh, preventing refresh from re-enabling an operator-disabled credential.
- Medium, conditional on OAuth interception: ea0b4712 uses a 256-bit Claude OAuth state and preserves case-sensitive state checking; inspect deployment and previous state generation before assigning a formal severity.
- Medium availability: dde469d3 probes Unix plugin loading in a subprocess to prevent a bad native plugin crashing the gateway; 0a94b2ae caps deferred response body buffering. Assess exploitability against actual deployment before treating these as vulnerabilities.
- Credential integrity and availability, severity undetermined: 68cd7572 prevents replay of a refresh grant after an uncertain outcome using durable claims and recovery receipts. e5827065 prevents disabled credential refresh spin. 1d4e0549 stops terminal request responses from causing inappropriate credential retries, refreshes and cooldowns.

This is a preliminary list, not a completed security review: upstream notes and commit titles were scanned but every patch and dependency advisory has not been assessed. No CVE or upstream severity was established.

An attempted non-committing merge of v8.0.60 into fork head produced conflicts in go.sum, management OAuth provider and v8 handlers, xAI auth imports, v8 config mapping, Codex auth tests, Antigravity model discovery, and conductor credential lifecycle. Key semantic overlaps requiring careful integration:

- f3762c64 moves Antigravity discovery to sdk/auth; the fork's existing discovery wraps its transport in GuardHubTransport. Preserve the guard and endpoint and response size constraints when adapting the extracted implementation.
- d3899771 and 482efbf5 change targeted OAuth recovery and provider metadata while the fork guards shared-authority enrollment and verifies recovery adoption; preserve both sets of guarantees.
- e5827065 and upstream lifecycle changes overlap the fork's authority generation, metadata and disabled-state preservation. Verify refresh fencing, holds and account selection with targeted tests.
- ec14dc47 adds kv-store configuration while the fork adds antigravity-keys; preserve both mappings.
- The dependency checksum conflict includes the fork's newer x/* versions; reconcile with the resulting go.mod rather than downgrading blindly.

The attempted integration was aborted without a commit because conflicts were unresolved. No rebased binary, tests or spare-port check were produced. No live m6 state was touched. This branch is an investigation record, not a cutover candidate.
