# Model User Whitelist Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-model user ID allowlists that gate requests before channel selection and hide restricted models from every user-facing model list while preserving existing token, group, and ability permissions.

**Architecture:** Store the allowlist as a JSON array in a TEXT column on `models`. Build one cached model-access policy matcher that applies exact, prefix, suffix, and contains metadata rules, then reuse it from the distributor and list controllers. Put the request check immediately after model parsing and keep admin model configuration pages unfiltered.

**Tech Stack:** Go 1.22, Gin, GORM v2, SQLite/MySQL/PostgreSQL, project JSON wrappers in `common/json.go`, React 18, Semi UI, Bun.

---

### Task 1: Add model allowlist storage and policy matcher

**Files:**
- Modify: `model/model_meta.go` — add persisted allowlist field and normalization.
- Create: `model/model_access.go` — cache metadata policies and expose access/filter helpers.
- Test: `model/model_access_test.go` — rule precedence, normalization, and roles.

- [ ] **Step 1: Write failing policy tests**

Create an in-memory SQLite database with exact, prefix, suffix, contains, empty, and restricted `Model` rows. The tests must exercise `IsModelAccessible` and `FilterModelsForUser` for ordinary users, listed users, admins, and root; exact must win over all other rules.

- [ ] **Step 2: Run focused tests to verify failure**

Run `go test ./model -run 'Test(IsModelAccessible|FilterModelsForUser)' -count=1`; expect a compile failure because the functions do not exist.

- [ ] **Step 3: Add storage and normalization**

Extend `model.Model` with:

```go
UserWhitelist []int `json:"user_whitelist,omitempty" gorm:"type:text;serializer:json"`
```

Normalize on create/update by removing duplicates and ignoring IDs `<= 0`. Empty/nil means unrestricted. Use `common.Marshal`/`common.Unmarshal` for any explicit JSON handling.

- [ ] **Step 4: Implement the policy API**

Add:

```go
func IsModelAccessible(modelName string, userID int, role int) (bool, error)
func FilterModelsForUser(modelNames []string, userID int, role int) ([]string, error)
func InvalidateModelAccessCache()
```

Use a mutex-protected metadata cache refreshed with pricing/model updates. Match `NameRuleExact`, `NameRulePrefix`, `NameRuleSuffix`, then `NameRuleContains`; missing or empty policy allows; root bypasses; admins do not. Normalize compact suffixes before matching. Malformed persisted policy must be treated as restricted for non-root users and returned as an error to callers.

- [ ] **Step 5: Run and commit**

Run the focused test command again; expect PASS. Commit with `git add model/model_meta.go model/model_access.go model/model_access_test.go && git commit -m "feat: add model user whitelist policy"`.

### Task 2: Propagate the user role through auth context

**Files:**
- Modify: `model/user.go`, `model/user_cache.go` — include role in cached user data and `WriteContext`.
- Modify: `constant/context_key.go`, `middleware/auth.go` — define/use a role context value for token and session requests.
- Test: existing user-cache/auth tests or `model/user_cache_test.go` — root/admin role propagation.

- [ ] **Step 1: Write the failing role test**

Build a root and an admin user, call the cache conversion and `WriteContext`, and assert the Gin context receives the exact role values. Verify an old cache entry without role falls back to the database path rather than becoming root.

- [ ] **Step 2: Implement role propagation**

Add `Role int` to `UserBase`, populate it in `ToBaseUser`, restore/write it in cache/context, and keep existing invalidation behavior. Session auth already carries `role`; make token auth expose the same context value.

- [ ] **Step 3: Run and commit**

Run `go test ./model ./middleware -run 'Test.*(Role|Auth|Cache)' -count=1`; expect PASS. Commit with `git add model/user.go model/user_cache.go constant/context_key.go middleware/auth.go && git commit -m "feat: expose user role in request context"`.

### Task 3: Enforce the policy before distributor routing

**Files:**
- Modify: `types/error.go` — add `ErrorCodeModelAccessDenied`.
- Modify: `i18n/keys.go`, `i18n/locales/en.yaml`, `i18n/locales/zh-CN.yaml` — add denial text.
- Modify: `middleware/distributor.go` — guard after model parsing and before channel selection.
- Test: `middleware/model_access_test.go` — response and ordering tests.

- [ ] **Step 1: Write failing middleware tests**

Route a request through `Distribute` with an allowlisted model and fake channel selector. Assert an unauthorized request returns status 403 with code `model_access_denied`, calls no selector, and does not reach the downstream handler. Cover ordinary token, specified-channel token, listed user, root, admin-not-listed, and compact suffix cases.

- [ ] **Step 2: Implement the guard**

Immediately after `getModelRequest` returns, before the `specific_channel_id` branch or any affinity/random channel lookup, call `model.IsModelAccessible`. On denial use:

```go
abortWithOpenAiMessage(c, http.StatusForbidden,
    i18n.T(c, i18n.MsgDistributorModelAccessDenied),
    types.ErrorCodeModelAccessDenied)
```

Abort policy read errors without routing. Leave token group/model-limit and Ability checks unchanged for allowed requests.

- [ ] **Step 3: Run and commit**

Run `go test ./middleware ./types -run 'Test.*(ModelAccess|Distributor)' -count=1`; expect PASS. Commit with `git add types/error.go i18n/keys.go i18n/locales/en.yaml i18n/locales/zh-CN.yaml middleware/distributor.go middleware/model_access_test.go && git commit -m "feat: enforce model user whitelist before routing"`.

### Task 4: Filter every user-facing model list

**Files:**
- Modify: `controller/model.go` — filter OpenAI/Anthropic/Gemini lists and singular retrieval.
- Modify: `controller/user.go` — filter `/api/user/self/models`.
- Test: `controller/model_list_test.go` and a focused user-model controller test.

- [ ] **Step 1: Write failing list tests**

Seed enabled abilities and metadata for unrestricted, restricted, and allowlisted models. Exercise `ListModels`, `DashboardListModels`, `GetUserModels`, and `RetrieveModel` for ordinary, listed, admin-not-listed, and root users. Assert unauthorized responses omit the restricted model and the singular endpoint returns the same 403/code.

- [ ] **Step 2: Apply the shared filter**

After existing token-model-limit, group/auto-group, and billing filters produce model names, call `model.FilterModelsForUser`; preserve ordering and response shapes. Use context role for TokenAuth and session role for dashboard/Playground. Do not filter admin `/api/models` or `/api/models/search` configuration endpoints.

- [ ] **Step 3: Run and commit**

Run `go test ./controller -run 'Test(ListModels|GetUserModels|RetrieveModel).*' -count=1`; expect PASS. Commit with `git add controller/model.go controller/user.go controller/model_list_test.go controller/user_models_test.go && git commit -m "feat: hide restricted models from user lists"`.

### Task 5: Add the admin model editor field

**Files:**
- Modify: `web/src/components/table/models/modals/EditModelModal.jsx` — edit, normalize, submit, and display IDs.
- Modify: all `web/src/i18n/locales/*.json` files — add labels/help text.

- [ ] **Step 1: Add the form field**

Initialize `user_whitelist` as `[]`, map API data to an array on load, and add a Semi `Form.TagInput`. Normalize comma-separated input to unique positive integer strings for display and convert to integers before POST/PUT. Submit `[]` when empty.

- [ ] **Step 2: Preserve create/update behavior**

Include `user_whitelist` in `submitData` for both create and update; leave existing tags/endpoints/status conversions unchanged. Explain that empty means all users and Root bypasses the restriction.

- [ ] **Step 3: Run frontend checks**

From `web/`, run `bun run i18n:lint` and `bun run build`; expect PASS. Commit with `git add web/src/components/table/models/modals/EditModelModal.jsx web/src/i18n/locales && git commit -m "feat: add model user whitelist editor"`.

### Task 6: Full verification and regression coverage

**Files:** affected files from Tasks 1–5; only modify tests if verification exposes a missing regression case.

- [ ] **Step 1: Format and run focused backend suites**

Run `gofmt -w` on changed Go files, then `go test ./model ./middleware ./controller`; expect PASS.

- [ ] **Step 2: Run the full Go suite**

Run `go test ./...`; expect PASS with the supported local database setup.

- [ ] **Step 3: Run frontend checks after integration**

From `web/`, run `bun run i18n:lint` and `bun run build`; expect PASS.

- [ ] **Step 4: Inspect the final diff**

Run `git diff --check`, `git status --short`, and `git diff --stat`. Confirm no protected project identity text changed, no direct JSON marshal/unmarshal calls were added to business code, and no admin configuration endpoint was filtered. Commit any necessary test-only fixes with a focused message.
