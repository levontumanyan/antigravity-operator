# Privacy Policy & Data Protection (LGPD & GDPR Compliance)

Antigravity Operator (`agyo`) is committed to the highest standards of data privacy, user sovereignty, and security. This document details our architecture, zero-telemetry guarantee, and adherence to the Brazilian General Data Protection Law (**LGPD - Lei Federal nº 13.709/2018**) and the General Data Protection Regulation (**GDPR - Regulation (EU) 2016/679**).

---

## 1. Core Principle: Privacy by Design & Local-First

Antigravity Operator is built from the ground up as a **100% offline, local-first developer tool**:

- **Zero Telemetry:** `agyo` contains no analytics, tracking beacons, pingbacks, or external network requests.
- **Zero Cloud Storage:** All data produced by `agyo` is stored exclusively on your local workstation's filesystem.
- **No Third-Party Data Sharing:** Your code, prompts, session states, and metadata are never collected or sent to any centralized server or third party by this project.

---

## 2. Adherence to LGPD Principles (Art. 6º, Lei 13.709/2018)

| LGPD Principle | How `agyo` Complies |
| :--- | :--- |
| **Finalidade (Purpose)** | Data processing is strictly confined to local session management, developer ergonomics, and tooling orchestration. |
| **Adequação (Suitability)** | Only operational metadata (task checklists, architectural notes, run status) is recorded. |
| **Necessidade (Data Minimization)** | No Personally Identifiable Information (PII), biometric data, sensitive credentials, or payment data is required or collected. |
| **Livre Acesso & Transparência (Access & Transparency)** | All generated files (`.agents/session/state.md`, `decisions.md`, `todo.md`) are human-readable Markdown files stored locally in your project folder. |
| **Segurança & Prevenção (Security & Prevention)** | Dedicated browser profile isolation prevents the agent from reading or leaking your personal browser cookies, saved passwords, or private bookmarks. |
| **Responsabilização (Accountability)** | Complete auditability through local git versioning and clear file structures. |

---

## 3. Data Sovereignty & Subject Rights (Art. 18 LGPD)

Because all state is stored locally on your machine, you retain complete sovereignty over your data:

- **Immediate Elimination (Direito de Exclusão):** You can permanently erase all session data at any time by running `agyo session archive` or simply deleting the local folder:
  ```bash
  rm -rf .agents/session
  rm -rf ~/.gemini/antigravity-browser-profile
  ```
- **Portability (Portabilidade):** Since data is plain-text Markdown and standard JSON, you can freely transfer, backup, or inspect it without proprietary lock-in.

---

## 4. Browser Isolation & Credential Protection

When launching automated browser sessions via `agyo browser`:

1. **Isolated User Data Directory:** The browser runs inside `~/.gemini/antigravity-browser-profile`, separate from your primary Chrome user profile (`Default` / `Profile 1`).
2. **Cookie & History Segregation:** Your personal browsing history, enterprise SSO cookies, and personal Google accounts remain completely isolated from automation tasks.
3. **Remote Debugging Safety:** The Chrome DevTools Protocol (CDP) port is bound to `127.0.0.1:9222` (loopback only) and is never exposed to external network interfaces.

---

## 5. Tool Call Analytics, Secret Redaction & Log Safety

When running `agyo session analytics` or viewing the dashboard's Tool Analytics & Loops tab:

1. **Local-Only Transcript Inspection:** Analytics processes local JSONL session files (`~/.gemini/.../transcript.jsonl`) entirely in-memory on the local machine without remote transmission.
2. **Automated Secret Redaction:** Commands, summaries, and parameters are automatically sanitized prior to display: common authentication headers (`Authorization: Bearer ...`), personal access tokens (`ghp_...`, `github_pat_...`), AWS credentials (`AKIA...`), and generic passwords/keys are scrubbed and replaced with `[REDACTED]`.
3. **Truncation & DOM Capping:** Commands and tool call summaries are capped to ~120 characters, lines over 10 MB in transcripts are skipped gracefully, and dashboard display records are limited to the latest 100 tool calls (newest first) to preserve browser performance.
4. **Prompt Context Protection:** Full user prompt text is excluded from the `/api/all` JSON payload by default to prevent accidental credential or prompt leakage over unauthenticated interfaces, and is only available opt-in via the CLI flag `--show-prompt`.

---

## 6. Best Practices for Developers Handling Sensitive Data

While `agyo` itself collects no personal data, developers using AI agents on enterprise repositories should observe the following guidelines:

1. **Do Not Commit Real PII:** Never paste customer CPFs, real phone numbers, credit card numbers, or passwords into session notes (`state.md` / `decisions.md`).
2. **Use Synthetic Data:** When testing database queries or browser flows with the agent, use mock fixtures, factory generators, or anonymized datasets.
3. **Exclude Session Files When Required:** If your project guidelines prohibit committing session state to version control, add `.agents/session/` to your repository's `.gitignore`.

---

## 7. Contact & Data Protection Officer (DPO)

For questions, security concerns, or privacy inquiries regarding Antigravity Operator:

- **Maintainer:** Tiago Vilas Boas
- **Email:** `tcarvalhovb@gmail.com`
- **Security Policy:** [SECURITY.md](SECURITY.md)
