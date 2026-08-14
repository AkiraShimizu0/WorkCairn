const INTERACTION_VERSION = "workspace-interaction.v1";
const COMMAND_VERSION = "workspace-command.v1";
const STORAGE_SESSION = "workcairn.active-session";
const STORAGE_PENDING = "workcairn.pending-command";
const STORAGE_NAV = "workcairn.active-nav";
const STORAGE_ERROR_PREFIX = "workcairn.last-error.";
const LOCAL_PROVIDER_SETUP_TIMEOUT_MS = 180000;
const DESKTOP_QUERY = "(min-width: 900px)";
const LIAISON_ROLE = "Product Manager";

const ui = {
  pairingView: document.querySelector("#pairing-view"),
  pairingForm: document.querySelector("#pairing-form"),
  workspaceView: document.querySelector("#workspace-view"),
  menuButton: document.querySelector("#menu-button"),
  navDrawer: document.querySelector("#nav-drawer"),
  navBackdrop: document.querySelector("#nav-backdrop"),
  navEmployeesHome: document.querySelector("#nav-employees-home"),
  navRequestList: document.querySelector("#nav-request-list"),
  navNewRequest: document.querySelector("#nav-new-request"),
  navCurrentRequest: document.querySelector("#nav-current-request"),
  navSettings: document.querySelector("#nav-settings"),
  requestsPane: document.querySelector("#requests-pane"),
  requestListView: document.querySelector("#request-list-view"),
  requestDetailView: document.querySelector("#request-detail-view"),
  requestSummary: document.querySelector("#request-summary"),
  backToListButton: document.querySelector("#back-to-list-button"),
  employeesPane: document.querySelector("#employees-pane"),
  status: document.querySelector("#connection-status"),
  backgroundStatus: document.querySelector("#background-status"),
  settingsButton: document.querySelector("#settings-button"),
  activeCard: document.querySelector("#active-card"),
  quickReplies: document.querySelector("#quick-replies"),
  threadScroll: document.querySelector("#thread-scroll"),
  threadJumpLatest: document.querySelector("#thread-jump-latest"),
  threadComposer: document.querySelector("#thread-composer"),
  composerStatus: document.querySelector("#composer-status"),
  composerInput: document.querySelector("#composer-input"),
  composerSend: document.querySelector("#composer-send"),
  detailsPanel: document.querySelector("#details-panel"),
  details: document.querySelector("#details-content"),
  sessionList: document.querySelector("#session-list"),
  timeline: document.querySelector("#activity-timeline"),
  employeeGrid: document.querySelector("#employee-grid"),
  companyFeed: document.querySelector("#company-feed"),
  teamCount: document.querySelector("#team-count"),
  attentionGrid: document.querySelector("#attention-grid"),
  autonomySummary: document.querySelector("#autonomy-summary"),
  proofOfWork: document.querySelector("#proof-of-work"),
  requestDialog: document.querySelector("#request-dialog"),
  requestForm: document.querySelector("#new-request-inline #request-form"),
  actionDialog: document.querySelector("#action-dialog"),
  actionForm: document.querySelector("#action-form"),
  settingsDialog: document.querySelector("#settings-dialog"),
  providerSettings: document.querySelector("#provider-settings"),
  storageSettings: document.querySelector("#storage-settings"),
  setupDialog: document.querySelector("#setup-dialog"),
  setupContent: document.querySelector("#setup-content"),
  busy: document.querySelector("#busy-overlay"),
  busyTitle: document.querySelector("#busy-title"),
  busyMessage: document.querySelector("#busy-message"),
  toast: document.querySelector("#toast"),
};

const state = {
  sessions: [],
  record: null,
  next: null,
  organization: null,
  companyActivity: null,
  evidence: new Map(),
  workReport: null,
  workReportError: null,
  providerStatus: null,
  providerSetupError: null,
  workspaceStatus: null,
  localSetupAvailable: false,
  lastError: null,
  nav: "employees_home",
  renderKey: "",
  detailRenderKey: "",
  timelineRenderKey: "",
  activeCommandID: "",
  commandInFlight: false,
  busy: false,
  pendingStart: null,
  threadNearBottom: true,
  forceScrollToBottom: false,
  pendingAttentionTitle: "",
  workflowPlanPreview: null,
  clarificationDraft: null,
};

class APIError extends Error {
  constructor(message, status, detail = null) {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.detail = detail;
  }
}

function node(tag, attributes = {}, ...children) {
  const element = document.createElement(tag);
  for (const [key, value] of Object.entries(attributes)) {
    if (key === "class") element.className = value;
    else if (key === "text") element.textContent = value;
    else if (key === "dataset") Object.assign(element.dataset, value);
    else if (key.startsWith("on") && typeof value === "function") element.addEventListener(key.slice(2), value);
    else if (value === true) element.setAttribute(key, "");
    else if (value !== false && value != null) element.setAttribute(key, String(value));
  }
  for (const child of children.flat()) {
    if (child == null) continue;
    element.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return element;
}

function button(label, kind, handler, type = "button") {
  return node("button", { class: `button ${kind}`, type, onclick: handler }, label);
}

function now() { return new Date().toISOString(); }
function secureRandomUUID() {
  const cryptoAPI = window.crypto;
  if (!cryptoAPI || typeof cryptoAPI.getRandomValues !== "function") {
    throw new APIError("このブラウザでは安全な依頼IDを作成できません。ブラウザを更新して再度お試しください。", 0, {
      code: "BROWSER_SECURE_RANDOM_UNAVAILABLE",
    });
  }
  const bytes = new Uint8Array(16);
  cryptoAPI.getRandomValues(bytes);
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, "0"));
  return `${hex.slice(0, 4).join("")}-${hex.slice(4, 6).join("")}-${hex.slice(6, 8).join("")}-${hex.slice(8, 10).join("")}-${hex.slice(10).join("")}`;
}

function commandID() { return `CMD-${secureRandomUUID().toUpperCase()}`; }
function sessionID() { return `SESSION-${Date.now()}-${secureRandomUUID().slice(0, 8).toUpperCase()}`; }
function projectID() { return `PROJECT-${Date.now()}-${secureRandomUUID().slice(0, 6).toUpperCase()}`; }

function stateLabel(value) {
  const labels = {
    plan_generation_approval_required: "進め方の作成待ち",
    clarification_required: "回答待ち",
    plan_approval_required: "進め方の承認待ち",
    ready_to_execute: "実行承認待ち",
    workflow_attention_required: "確認が必要",
    completed: "完了",
    action_completed: "完了",
    action_attention_required: "確認が必要",
    waiting: "待機中",
    standby: "必要時に参加",
    blocked: "確認が必要",
  };
  return labels[value] || value || "確認中";
}

function sameCalendarDay(left, right) {
  return left.getFullYear() === right.getFullYear()
    && left.getMonth() === right.getMonth()
    && left.getDate() === right.getDate();
}

function sessionDateGroupLabel(value) {
  const date = new Date(value);
  const today = new Date();
  const yesterday = new Date();
  yesterday.setDate(today.getDate() - 1);
  if (sameCalendarDay(date, today)) return "今日";
  if (sameCalendarDay(date, yesterday)) return "昨日";
  return date.toLocaleDateString("ja-JP", { year: "numeric", month: "long", day: "numeric" });
}

function sessionTimeLabel(value) {
  return new Date(value).toLocaleTimeString("ja-JP", { hour: "2-digit", minute: "2-digit" });
}

function liaisonIdentity() {
  return employeeIdentityByRole(LIAISON_ROLE);
}

function liaisonMessage(text, at, extras = {}) {
  return { side: "employee", identity: liaisonIdentity(), text, at, liaison: true, ...extras };
}

function liaisonRequestLabel() {
  const liaison = liaisonIdentity();
  if (liaison.name && liaison.name !== "AI社員") return `${liaison.name}に依頼する`;
  return "窓口社員に依頼する";
}

function workcairnEvent(text, at, extras = {}) {
  return { side: "system", speaker: "WorkCairn", text, at, ...extras };
}

function sessionListPresentation(record) {
  const liaison = liaisonIdentity();
  const activeID = state.record?.session_id;
  const isActive = activeID === record.session_id;
  let hasError = false;
  try { hasError = Boolean(JSON.parse(localStorage.getItem(errorStorageKey(record.session_id)) || "null")); } catch {}
  if (hasError) return { icon: "warning", label: "確認が必要です" };
  if (isActive && state.next && !state.lastError) {
    switch (state.next.kind) {
    case "answer_clarifications":
    case "approve_plan_generation":
    case "approve_plan_apply":
    case "approve_workflow":
      return { icon: "attention", label: "確認が必要です" };
    case "inspect_workflow_recovery":
    case "inspect_action_recovery":
      return { icon: "warning", label: "確認が必要です" };
    case "done":
    case "optional_external_action_or_done":
      return { icon: "complete", label: "完了" };
    default:
      break;
    }
  }
  switch (record.state) {
  case "completed":
  case "action_completed":
    return { icon: "complete", label: "完了" };
  case "workflow_attention_required":
  case "action_attention_required":
    return { icon: "warning", label: "確認が必要です" };
  case "clarification_required":
  case "plan_approval_required":
  case "ready_to_execute":
  case "plan_generation_approval_required":
    return { icon: "attention", label: "確認が必要です" };
  default: {
    const lastTurn = record.turns?.at(-1);
    if (lastTurn?.workflow?.status === "completed" || lastTurn?.kind === "plan_generated") {
      return { icon: "working", label: "新しい進捗があります" };
    }
    return { icon: "working", label: "作業中" };
  }
  }
}

function sessionIconNode(icon) {
  const labels = { attention: "対応待ち", working: "作業中", complete: "完了", warning: "要確認" };
  return node("span", { class: `session-icon session-icon-${icon}`, "aria-label": labels[icon] || icon },
    icon === "complete" ? "✓" : "",
  );
}

function timelineStageLabel(stage) {
  const labels = {
    依頼: "依頼",
    clarification: "確認",
    plan: "進め方",
    approval: "承認",
    execution: "作業",
    review: "レビュー",
    revision: "修正",
    completion: "完了",
    failure: "停止",
  };
  return labels[stage] || stage || "";
}

function groupSessionsByDate(sessions) {
  const groups = [];
  const indexByLabel = new Map();
  for (const record of sessions) {
    const label = sessionDateGroupLabel(record.created_at);
    if (!indexByLabel.has(label)) {
      indexByLabel.set(label, groups.length);
      groups.push({ label, items: [] });
    }
    groups[indexByLabel.get(label)].items.push(record);
  }
  return groups;
}

function technicalDetails(summary, facts) {
  return node("details", { class: "technical-details" },
    node("summary", {}, summary),
    approvalFacts(facts),
  );
}

function iconButton(label, glyph, handler, className = "icon-button") {
  return node("button", {
    class: className,
    type: "button",
    "aria-label": label,
    title: label,
    onclick: handler,
  }, glyph);
}

function messageFactsFromDetail(detail) {
  if (!detail) return [];
  if (Array.isArray(detail)) return detail;
  return String(detail).split("\n").filter(Boolean).map((line) => {
    const split = line.indexOf(":");
    if (split <= 0) return ["Detail", line];
    return [line.slice(0, split).trim(), line.slice(split + 1).trim()];
  });
}

function inlineMessageActions(message) {
  const facts = messageFactsFromDetail(message.detail);
  if (!facts.length && !message.onCopy) return null;
  const panel = node("div", { class: "msg-technical-panel", hidden: true });
  if (facts.length) panel.append(approvalFacts(facts));
  const toggle = iconButton("技術的な詳細", "ⓘ", () => { panel.hidden = !panel.hidden; }, "icon-button msg-info-toggle");
  const copy = message.onCopy
    ? iconButton("詳細をコピー", "⎘", message.onCopy, "icon-button msg-copy-toggle")
    : null;
  return node("div", { class: "msg-actions" }, toggle, copy, panel);
}

function setQuickReplies(buttons = []) {
  if (!buttons.length) {
    ui.quickReplies.replaceChildren();
    ui.quickReplies.hidden = true;
    return;
  }
  ui.quickReplies.hidden = false;
  ui.quickReplies.replaceChildren(node("div", { class: "quick-replies-row" }, ...buttons));
}

function clearActionSurface() {
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
  setQuickReplies([]);
}

function resetClarificationDraft() {
  state.clarificationDraft = null;
}

function clarificationDraft(next) {
  const key = JSON.stringify([next.session_id, next.expected_version, next.questions]);
  if (!state.clarificationDraft || state.clarificationDraft.key !== key) {
    state.clarificationDraft = { key, answers: [], index: 0 };
  }
  return state.clarificationDraft;
}

function activeEmployeeStatuses() {
  return (state.companyActivity?.employees || []).filter((employee) =>
    ["作業中", "レビュー中", "修正中"].includes(employee.display_status),
  );
}

function employeeActivityStatusText(employee) {
  const name = employee.name || employee.id || "AI社員";
  switch (employee.display_status) {
  case "作業中":
    return employee.current_work_title ? `${name}が${employee.current_work_title}を作成しています` : `${name}が作業しています`;
  case "レビュー中":
    return employee.current_work_title ? `${name}が${employee.current_work_title}をレビューしています` : `${name}がレビューしています`;
  case "修正中":
    return employee.current_work_title ? `${name}が${employee.current_work_title}の修正版を確認しています` : `${name}が修正しています`;
  default:
    return "";
  }
}

function composerStatusText(next) {
  if (state.lastError) return "確認が必要です";
  const pending = storedPendingCommand();
  if (pending) {
    const copy = inFlightCopy(pending.operation);
    return copy.title || copy.label;
  }
  if (!next) return "依頼を選択してください";

  const activeEmployees = activeEmployeeStatuses();
  if (activeEmployees.length === 1) {
    const status = employeeActivityStatusText(activeEmployees[0]);
    if (status) return status;
  } else if (activeEmployees.length > 1) {
    return `${activeEmployees.length}人の社員が作業しています`;
  }

  if (next.kind === "answer_clarifications") {
    const total = next.questions.length;
    if (total <= 1) return "回答を入力してください";
    const draft = clarificationDraft(next);
    return `質問 ${draft.index + 1} / ${total} に回答してください`;
  }
  if (next.kind === "approve_plan_generation") return "進め方の作成承認待ちです";
  if (next.kind === "approve_plan_apply") return "進め方の承認待ちです";
  if (next.kind === "approve_workflow") return "実行承認待ちです";
  if (next.kind === "done" || next.kind === "optional_external_action_or_done") return "完了しました";
  if (next.kind === "inspect_workflow_recovery" || next.kind === "inspect_action_recovery") return "確認が必要です";

  const liaison = (state.companyActivity?.employees || []).find((employee) =>
    employee.is_liaison && employee.display_status === "社長と相談中",
  );
  if (liaison?.name) return `${liaison.name}が社長の回答待ちです`;
  return "作業を進めています";
}

function composerCapabilities(next) {
  if (!next || state.lastError) {
    return { enabled: false, placeholder: "メッセージを入力...", mode: "idle" };
  }
  if (storedPendingCommand()) {
    return { enabled: false, placeholder: "メッセージを入力...", mode: "running" };
  }
  if (next.kind === "answer_clarifications") {
    return { enabled: true, placeholder: "回答を入力...", mode: "clarification" };
  }
  return { enabled: false, placeholder: "メッセージを入力...", mode: "idle" };
}

function renderComposerState(next) {
  const capabilities = composerCapabilities(next);
  ui.composerStatus.textContent = composerStatusText(next);
  ui.composerInput.placeholder = capabilities.placeholder;
  ui.composerInput.readOnly = !capabilities.enabled;
  ui.composerInput.setAttribute("aria-readonly", capabilities.enabled ? "false" : "true");
  ui.composerSend.disabled = !capabilities.enabled;
  ui.threadComposer.dataset.mode = capabilities.mode;
  ui.threadComposer.classList.toggle("composer-idle", !capabilities.enabled);
}

function isThreadNearBottom() {
  const element = ui.threadScroll;
  if (!element) return true;
  return element.scrollHeight - element.scrollTop - element.clientHeight < 96;
}

function scrollThreadToBottom() {
  const element = ui.threadScroll;
  if (!element) return;
  element.scrollTop = element.scrollHeight;
  state.threadNearBottom = true;
  ui.threadJumpLatest.hidden = true;
}

function showJumpToLatest(visible) {
  if (!ui.threadJumpLatest) return;
  ui.threadJumpLatest.hidden = !visible;
}

function shortDigest(value) {
  if (!value) return "—";
  return value.length > 28 ? `${value.slice(0, 20)}…${value.slice(-6)}` : value;
}

async function requestJSON(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Accept", "application/json");
  if (options.body != null) {
    headers.set("Content-Type", "application/json");
    headers.set("X-Workspace-Intent", "mobile-ui.v1");
  }
  let response;
  try {
    response = await fetch(path, { ...options, headers, credentials: "same-origin" });
  } catch (error) {
    throw new APIError("Macとの通信が切れました。状態を確認してから再開してください。", 0, error);
  }
  const payload = await response.json().catch(() => null);
  if (!response.ok || payload?.ok === false) {
    const code = payload?.error?.code || payload?.error || `HTTP_${response.status}`;
    // Synchronous commands (e.g. interaction.plan.generate) return their full
    // typed result alongside the minimal error envelope on failure. Merge the
    // Provider/parse diagnostics from that result into detail so showError()
    // sees the same fields it already reads from polled async Command status.
    const detail = payload?.error
      ? { ...payload.error, ...errorDiagnostics(payload.error.details, payload.result) }
      : payload;
    throw new APIError(String(code), response.status, detail);
  }
  return payload && Object.hasOwn(payload, "result") ? payload.result : payload;
}

function setConnected(connected) {
  ui.status.textContent = connected ? "Macに接続中" : "接続を確認してください";
  ui.status.classList.toggle("online", connected);
}

function setBusy(active, title = "処理しています", message = "この画面を開いたままお待ちください。") {
  state.busy = active;
  ui.busy.hidden = !active;
  ui.busyTitle.textContent = title;
  ui.busyMessage.textContent = message;
}

function setBackgroundWorking(active) {
  ui.backgroundStatus.hidden = !active;
  ui.backgroundStatus.setAttribute("aria-label", active ? "バックグラウンドで実行中" : "");
}

function inFlightCopy(operation) {
  switch (operation) {
  case "interaction.plan.generate":
    return { label: "進め方を考えています", title: "企画担当が進め方を考えています", message: "質問または進め方ができるまで、Macで処理を続けます。" };
  case "interaction.workflow.execute":
    return { label: "仕事を進めています", title: "AI社員が仕事を進めています", message: "Makerの成果物作成、QA担当のReview、必要なRevisionを順番に進めます。" };
  default:
    return { label: "処理中", title: "会社が仕事を進めています", message: "承認済みの処理をMacで安全に続けています。" };
  }
}

function renderInFlight(command) {
  const copy = inFlightCopy(command?.operation);
  clearActionSurface();
  renderComposerState(null);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("p", { class: "composer-note" }, copy.message),
    node("p", { class: "composer-note visually-hidden" }, "この画面を閉じても処理はMacで続きます。次に判断が必要になったら依頼詳細へ表示します。"),
  );
  state.forceScrollToBottom = true;
  renderTimeline();
}

function errorStorageKey(sessionID = state.record?.session_id) {
  return sessionID ? `${STORAGE_ERROR_PREFIX}${sessionID}` : "";
}

function rememberError(error, title, commandID = state.activeCommandID) {
  const detail = error instanceof APIError ? error.detail : null;
  const sessionID = state.record?.session_id || localStorage.getItem(STORAGE_SESSION);
  if (!sessionID) return null;
  const snapshot = {
    session_id: sessionID,
    session_version: state.record?.version || 0,
    title,
    code: detail?.code || error.message || "UNKNOWN_ERROR",
    stage: detail?.stage || "",
    command_id: detail?.command_id || commandID || "",
    request_id: detail?.provider_failure?.request_id || "",
    substage: detail?.details?.substage || "",
    category: detail?.details?.category || detail?.provider_failure?.category || "",
    http_status: detail?.provider_failure?.http_status || 0,
    parse_failure_reason: detail?.parse_failure_reason || "",
    parse_failure_field: detail?.parse_failure_field || detail?.details?.parse?.field || "",
    recovery_required: Boolean(detail?.recovery_required),
    // details is the additive, optional single failure.Envelope. Kept
    // alongside the flat fields above (still the legacy fallback source)
    // for the same migration-period reason as errorDiagnostics().
    details: detail?.details || null,
    at: now(),
  };
  try {
    const existing = JSON.parse(localStorage.getItem(errorStorageKey(sessionID)) || "null");
    if (existing && existing.session_version === snapshot.session_version && existing.title === snapshot.title &&
    existing.code === snapshot.code && existing.stage === snapshot.stage && existing.command_id === snapshot.command_id &&
        existing.request_id === snapshot.request_id && existing.substage === snapshot.substage &&
        existing.category === snapshot.category && existing.http_status === snapshot.http_status &&
        existing.parse_failure_reason === snapshot.parse_failure_reason &&
        existing.parse_failure_field === snapshot.parse_failure_field &&
        existing.recovery_required === snapshot.recovery_required &&
        JSON.stringify(existing.details) === JSON.stringify(snapshot.details)) {
      state.lastError = existing;
      return existing;
    }
  } catch {}
  localStorage.setItem(errorStorageKey(sessionID), JSON.stringify(snapshot));
  state.lastError = snapshot;
  return snapshot;
}

function restoreError(record) {
  state.lastError = null;
  if (!record) return;
  try {
    const stored = JSON.parse(localStorage.getItem(errorStorageKey(record.session_id)) || "null");
    if (!stored || stored.session_id !== record.session_id) return;
    if (stored.session_version && record.version > stored.session_version && !["workflow_attention_required", "action_attention_required"].includes(record.state)) {
      localStorage.removeItem(errorStorageKey(record.session_id));
      return;
    }
    state.lastError = stored;
  } catch {
    localStorage.removeItem(errorStorageKey(record.session_id));
  }
}

function clearCurrentError() {
  const key = errorStorageKey();
  if (key) localStorage.removeItem(key);
  state.lastError = null;
}

// structuredFieldsSummary renders a Structured Output key presence
// diagnostic (failure.Envelope.Parse.StructuredOutputPresence — booleans
// only, never a field's value) as a short "key: present/missing" list.
// Returns "" when there is nothing to show.
function structuredFieldsSummary(presence) {
  if (!presence || typeof presence !== "object") return "";
  const keys = Object.keys(presence).sort();
  if (keys.length === 0) return "";
  return keys.map((key) => `${key}: ${presence[key] ? "present" : "missing"}`).join(", ");
}

function structuredFieldShapeSummary(shapes, field) {
  const shape = shapes?.[field];
  if (!shape || typeof shape !== "object") return "";
  const parts = [`present: ${shape.present ? "yes" : "no"}`];
  if (shape.json_type) parts.push(`json_type: ${shape.json_type}`);
  if (typeof shape.non_blank === "boolean") parts.push(`non_blank: ${shape.non_blank ? "yes" : "no"}`);
  return parts.join(", ");
}

function parseDiagnosticsFacts(error) {
  const parse = error.details?.parse;
  const structuredFields = structuredFieldsSummary(parse?.structured_output_presence);
  const parseField = parse?.field || error.parse_failure_field || "—";
  const fieldShape = structuredFieldShapeSummary(parse?.structured_output_field_shape, parseField);
  return [
    ["Error code", error.code],
    ["Stage", error.stage || "—"],
    ["Substage", error.substage || "—"],
    ["Category", error.category || "—"],
    ["HTTP status", error.http_status || "—"],
    ["Command ID", error.command_id || "—"],
    ["Request ID", error.request_id || "—"],
    ["Parse reason", error.parse_failure_reason || parse?.reason || "—"],
    ["Parse field", parseField],
    ...(structuredFields ? [["Structured fields", structuredFields]] : []),
    ...(fieldShape ? [[`${parseField} shape`, fieldShape]] : []),
  ];
}

async function copySanitizedError(error) {
  const facts = parseDiagnosticsFacts(error);
  const detail = facts.map(([label, value]) => `${label}: ${value}`).join("\n");
  let copied = false;
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(detail);
      copied = true;
    } catch {}
  }
  if (!copied) copied = copyTextWithSelection(detail);
  if (copied) {
    toast("エラー詳細をコピーしました。");
    return;
  }
  showManualCopy(detail);
}

function copyTextWithSelection(detail) {
  const textarea = node("textarea", { readonly: true, "aria-label": "コピーするエラー詳細" }, detail);
  textarea.value = detail;
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  textarea.style.pointerEvents = "none";
  document.body.append(textarea);
  textarea.focus();
  textarea.select();
  textarea.setSelectionRange(0, textarea.value.length);
  let copied = false;
  try { copied = typeof document.execCommand === "function" && document.execCommand("copy"); } catch {}
  textarea.remove();
  return copied;
}

function showManualCopy(detail) {
  const textarea = node("textarea", { class: "copy-detail", readonly: true, "aria-label": "コピーするエラー詳細" }, detail);
  textarea.value = detail;
  const retryCopy = () => {
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    let copied = false;
    try { copied = typeof document.execCommand === "function" && document.execCommand("copy"); } catch {}
    if (copied) {
      toast("エラー詳細をコピーしました。");
      closeActionDialog();
      return;
    }
    toast("コピーできませんでした。詳細を長押ししてコピーしてください。");
  };
  ui.actionForm.onsubmit = null;
  ui.actionForm.replaceChildren(
    node("div", { class: "sheet-handle" }),
    node("div", { class: "sheet-heading" },
      node("div", {}, node("p", { class: "eyebrow" }, "ERROR DETAILS"), node("h2", {}, "詳細を選択してコピー")),
      node("button", { class: "icon-button", type: "button", "aria-label": "閉じる", onclick: closeActionDialog }, "×"),
    ),
    node("p", { class: "supporting" }, "自動コピーを利用できませんでした。下の安全な診断情報を長押ししてコピーしてください。"),
    textarea,
    node("div", { class: "sheet-actions" },
      button("選択内容をコピー", "primary", retryCopy),
      button("閉じる", "quiet", closeActionDialog),
    ),
  );
  if (!ui.actionDialog.open) ui.actionDialog.showModal();
  textarea.focus();
  textarea.select();
  toast("コピーできませんでした。詳細を選択してコピーしてください。");
}

function isDesktopLayout() {
  return window.matchMedia(DESKTOP_QUERY).matches;
}

function isRequestDetailVisible() {
  if (isDesktopLayout()) return Boolean(state.record);
  return state.nav === "request_detail" && Boolean(state.record);
}

function setNav(name, remember = true) {
  state.nav = name;
  if (remember) localStorage.setItem(STORAGE_NAV, name);
  applyNavigationLayout();
  updateNavDrawerState();
  if (isRequestDetailVisible() && state.record) {
    state.renderKey = "";
    renderNext(true);
  }
  if (name === "employees_home" || isDesktopLayout()) refreshEmployeesPane();
}

function applyNavigationLayout() {
  const desktop = isDesktopLayout();
  ui.menuButton.hidden = desktop;
  ui.requestsPane.classList.toggle("mobile-visible", desktop || state.nav === "request_list" || state.nav === "request_detail");
  ui.employeesPane.classList.toggle("mobile-hidden", !desktop && state.nav !== "employees_home");
  ui.requestListView.classList.toggle("mobile-hidden", !desktop && state.nav !== "request_list");
  ui.requestDetailView.hidden = desktop ? !state.record : state.nav !== "request_detail" || !state.record;
  ui.requestDetailView.classList.toggle("mobile-hidden", ui.requestDetailView.hidden);
  ui.requestListView.hidden = desktop ? Boolean(state.record) : state.nav !== "request_list";
  ui.requestListView.classList.toggle("mobile-hidden", ui.requestListView.hidden);
  setBackgroundWorking(Boolean(storedPendingCommand()) && !isRequestDetailVisible());
}

function openNavDrawer() {
  ui.navDrawer.hidden = false;
  ui.navBackdrop.hidden = false;
  ui.menuButton.setAttribute("aria-expanded", "true");
  ui.navCurrentRequest.hidden = !state.record;
}

function closeNavDrawer() {
  ui.navDrawer.hidden = true;
  ui.navBackdrop.hidden = true;
  ui.menuButton.setAttribute("aria-expanded", "false");
}

function updateNavDrawerState() {
  ui.navCurrentRequest.hidden = !state.record;
}

function showRequestList() {
  closeNavDrawer();
  if (isDesktopLayout()) {
    if (state.record) selectSession(null);
    else applyNavigationLayout();
    renderSessions();
    return;
  }
  setNav("request_list");
}

function showRequestDetail(sessionID = state.record?.session_id) {
  closeNavDrawer();
  if (sessionID && sessionID !== state.record?.session_id) {
    selectSession(sessionID);
    return;
  }
  if (isDesktopLayout()) {
    applyNavigationLayout();
    if (state.record) {
      state.renderKey = "";
      renderRequestDetail();
    }
    return;
  }
  setNav("request_detail");
}

function showEmployeesHome() {
  closeNavDrawer();
  setNav("employees_home");
}

let toastTimer;
function toast(message) {
  clearTimeout(toastTimer);
  ui.toast.textContent = message;
  ui.toast.hidden = false;
  toastTimer = setTimeout(() => { ui.toast.hidden = true; }, 4200);
}

function showError(error, title = "処理を完了できませんでした") {
  setBusy(false);
  setBackgroundWorking(false);
  const remembered = rememberError(error, title);
  const detail = error instanceof APIError ? error.detail : null;
  const code = detail?.code || error.message || "UNKNOWN_ERROR";
  const stage = detail?.stage;
  const providerSetupRequired = code === "PROVIDER_CONFIGURATION_REQUIRED";
  const providerGenerationFailed = code === "INTERACTION_PLAN_FAILED" && stage === "interaction_plan_generation";
  const planCommitConflict = code === "INTERACTION_PLAN_FAILED" && stage === "interaction_plan_commit_cas";
  // Covers every CEO Plan generation-side stage that can no longer produce
  // a safe Canonical Plan: the small Intent contract itself was malformed
  // ("ceo_plan_intent"), Go could not deterministically resolve an Employee
  // assignment ("ceo_plan_normalization"), or the Go-constructed candidate
  // still failed final canonical validation ("ceo_plan_parser", kept as
  // defense-in-depth). All three share the same safe, unapplied outcome.
  const planResponseInvalid = code === "INTERACTION_PLAN_FAILED" &&
    (stage === "ceo_plan_intent" || stage === "ceo_plan_normalization" || stage === "ceo_plan_parser");
  const workflowAssignmentRequired = code === "WORKFLOW_TASK_ASSIGNMENT_REQUIRED";
  const workflowReviewerRequired = code === "WORKFLOW_REVIEWER_ASSIGNMENT_REQUIRED";
  // Safety net for the rare case where automatic Project name disambiguation
  // itself was exhausted. The common case (same request sent twice) is
  // handled silently by creating a distinctly named Project, so this should
  // be uncommon; when it happens, never suggest editing a directory name.
  const projectNameCollision = code === "PROJECT_NAME_COLLISION";
  const providerFailures = {
    PROVIDER_AUTHENTICATION_REQUIRED: "Claudeの接続を確認してください。credentialが無効・失効している可能性があります。",
    PROVIDER_BILLING_REQUIRED: "Claude側の請求・支払い設定を確認してください。WorkCairnは自動retryしません。",
    PROVIDER_PERMISSION_DENIED: "Claude側で、この接続に必要な利用権限を確認してください。",
    PROVIDER_REQUEST_INVALID: "WorkCairnからClaudeへ送ったrequestが拒否されました。自動retryせず、問い合わせIDを確認してください。",
    PROVIDER_RATE_LIMITED: "Claudeの利用上限に達しました。時間を置き、状態を確認してから新しいCommandとして再開してください。",
    PROVIDER_UNAVAILABLE: "Claudeへ接続できないか、Claude側が一時的に利用できません。自動fallbackやretryは行っていません。",
    PROVIDER_RESPONSE_INVALID: "Claudeから正常に読み取れる応答を受け取れませんでした。自動retryせず、問い合わせIDを確認してください。",
  };
  const providerFailureCopy = providerFailures[code];
  // Non-Provider Review contract failures: the Runner responded, but its
  // structured Review result did not satisfy the typed Review contract
  // (missing/duplicate markers, invalid verdict, invalid issues shape, ...),
  // or the Prompt/route could not be built at all. These are deliberately
  // kept out of providerFailures so they never trigger the "open AI
  // Connections" action -- there is no connection problem to fix.
  const reviewContractFailures = {
    REVIEW_PROMPT_FAILED: "レビュー用の指示を組み立てられませんでした。成果物は保持されています。",
    REVIEW_ROUTE_FAILED: "レビュー担当のAIモデルを解決できませんでした。成果物は保持されています。",
    REVIEW_RESULT_INVALID: "AIのレビュー結果を正しく解釈できませんでした。成果物は保持されています。",
  };
  const reviewContractCopy = reviewContractFailures[code];
  const providerIssue = providerSetupRequired || providerGenerationFailed || Boolean(providerFailureCopy);
  const providerRequestID = detail?.provider_failure?.request_id;
  const parseFailureReason = detail?.parse_failure_reason;
  const providerSettingsAction = providerSetupRequired || code === "PROVIDER_AUTHENTICATION_REQUIRED" || code === "PROVIDER_PERMISSION_DENIED";
  const pending = sessionStorage.getItem(STORAGE_PENDING);
  const errorRenderKey = `error:${JSON.stringify([remembered?.session_id || "", remembered?.session_version || 0, code, stage || "", remembered?.command_id || ""] )}`;
  if (state.renderKey === errorRenderKey) return;
  state.renderKey = errorRenderKey;
  ui.activeCard.className = "conversation-composer action-card attention";
  const structuredFields = structuredFieldsSummary(detail?.details?.parse?.structured_output_presence);
  ui.activeCard.replaceChildren(
    conversationNode({ side: "system", speaker: "WorkCairn", text: title, attention: true }),
    node("p", { class: "composer-note" }, providerSetupRequired
      ? "AIサービスの接続設定が不足しています。Providerへ依頼は送信されていません。MacのAI Connectionsから接続してください。"
      : providerFailureCopy
        ? providerFailureCopy
      : reviewContractCopy
        ? reviewContractCopy
      : projectNameCollision
        ? "同じ名前の仕事がすでにあります。新しい仕事として作成できませんでした。少し時間を置くか、依頼の表現を変えて改めて送ってください。"
      : planResponseInvalid
        ? "AIサービスから応答を受信しましたが、安全な進め方として確認できる形式ではありませんでした。進め方は保存・適用されていません。"
      : planCommitConflict
        ? "同じ依頼の状態が先に更新されたため、この進め方は保存していません。新しい状態を確認してください。"
      : workflowAssignmentRequired
        ? "担当AIを決められない仕事があるため、実行を開始していません。Organizationを確認してから、改めて実行内容を確認してください。"
      : workflowReviewerRequired
        ? "Makerと異なるReviewerを一意に決められなかったため、Workflowを開始していません。OrganizationのQA Engineerを確認してください。"
      : providerGenerationFailed
        ? "AIサービスで進め方を生成できませんでした。自動retryや別サービスへの切替は行っていません。接続状態を確認してください。"
      : "成立済みの記録を推測で変更せず、現在の状態を確認してください。"),
    technicalDetails("技術的な詳細を見る", [
      ["Error code", code],
      ["Stage", stage || "—"],
      ["Substage", detail?.details?.substage || "—"],
      ["Category", detail?.category || detail?.details?.category || "—"],
      ["HTTP status", detail?.details?.provider?.http_status || detail?.http_status || "—"],
      ["Command ID", remembered?.command_id || detail?.command_id || "未発行"],
      ["問い合わせID", providerRequestID || detail?.provider_failure?.request_id || "—"],
      ["Parse reason", parseFailureReason || detail?.parse_failure_reason || "—"],
      ["Parse field", detail?.parse_failure_field || "—"],
      ...(structuredFields ? [["Structured fields", structuredFields]] : []),
    ]),
    node("div", { class: "button-row" },
      button(providerSettingsAction ? "AI Connectionsを開く" : (providerIssue ? "進め方の作成待ちへ戻る" : (pending ? "Command状態を再確認" : "状態を再確認")), "primary", () => providerSettingsAction ? openSettingsDialog() : (pending ? resumePendingCommand(pending) : refreshCurrent())),
      button("依頼一覧へ", "quiet", () => { selectSession(null); showRequestList(); }),
    ),
  );
  if (remembered) {
    renderTimeline();
    renderSessions();
  }
}

async function pair(event) {
  event.preventDefault();
  const code = new FormData(ui.pairingForm).get("code")?.toString().trim();
  if (!code) return;
  setBusy(true, "Macと接続しています", "ペアリングコードを確認しています。今回の起動中だけ有効です。");
  try {
    await requestJSON("/v1/local-access/pair", { method: "POST", body: JSON.stringify({ code }) });
    ui.pairingForm.reset();
    await startWorkspace();
  } catch (error) {
    setBusy(false);
    toast(error.status === 401 ? "ペアリングコードが一致しません。" : error.message);
  }
}

async function startWorkspace() {
  setBusy(false);
  ui.pairingView.hidden = true;
  ui.workspaceView.hidden = false;
  ui.settingsButton.hidden = false;
  ui.menuButton.hidden = isDesktopLayout();
  setConnected(true);
  await Promise.all([loadSessions(), loadProviderStatus(), loadWorkspaceStatus(), loadOrganization().catch(() => null), loadCompanyActivity().catch(() => null)]);
  const stored = localStorage.getItem(STORAGE_SESSION);
  const candidate = state.sessions.find((record) => record.session_id === stored) ||
    state.sessions.find((record) => !["completed", "action_completed"].includes(record.state)) || state.sessions[0];
  const preferredNav = localStorage.getItem(STORAGE_NAV);
  if (candidate?.session_id) {
    await selectSession(candidate.session_id, { rememberNav: false });
    if (preferredNav === "request_detail" || isDesktopLayout()) showRequestDetail(candidate.session_id);
    else if (preferredNav === "request_list" && !isDesktopLayout()) setNav("request_list", false);
    else setNav("employees_home", false);
  } else {
    setNav(preferredNav && preferredNav !== "request_detail" ? preferredNav : "employees_home", false);
  }
  if (state.workspaceStatus && (!state.workspaceStatus.organization_ready || !state.providerStatus?.configured)) openSetupWizard();
}

async function loadWorkspaceStatus() {
  try {
    state.workspaceStatus = await requestJSON("/v1/workspace-status");
  } catch {
    state.workspaceStatus = null;
  }
}

async function loadProviderStatus() {
  try {
    state.providerStatus = await requestJSON("/v1/provider-status");
  } catch {
    state.providerStatus = { configured: false, missing: [], invalid: ["status_unavailable"] };
  }
}

async function refreshProviderStatus() {
  setBusy(true, "AIサービスを確認しています", "credentialの値やProviderへの通信は行わず、起動時設定だけを確認します。");
  await loadProviderStatus();
  setBusy(false);
  if (state.record) renderNext(true);
  renderProviderSettings();
  renderStorageSettings();
  toast(state.providerStatus?.configured ? "AIサービスを利用できます。" : "MacのAI ConnectionsからClaudeを接続してください。");
}

async function connectClaudeOnMac() {
	const controller = new AbortController();
	let clientTimedOut = false;
	const timeout = window.setTimeout(() => {
		clientTimedOut = true;
		controller.abort();
	}, LOCAL_PROVIDER_SETUP_TIMEOUT_MS);
	setBusy(true, "MacでClaudeへ接続しています", "Macに表示される安全な入力画面を確認してください。secretはbrowserへ送信しません。");
	try {
		state.providerStatus = await requestJSON("/v1/local-setup/claude", { method: "POST", body: "{}", signal: controller.signal });
		state.providerSetupError = null;
		renderProviderSettings();
		if (state.workspaceStatus?.organization_ready) {
			if (ui.settingsDialog.open) ui.settingsDialog.close();
			openSetupWizard();
		} else if (ui.setupDialog.open) renderSetupWizard();
		toast("Claudeへ接続しました。RoutingはAutomaticです。");
	} catch (error) {
		state.providerSetupError = {
			code: error.detail?.code || error.message || "PROVIDER_CONNECTION_SETUP_FAILED",
			stage: error.detail?.stage || "provider_connection_setup",
			substage: error.detail?.details?.substage || "",
			category: clientTimedOut ? "keychain_setup_timeout" : (error.detail?.details?.category || ""),
		};
		renderProviderSettings();
		if (ui.setupDialog.open) renderSetupWizard();
		toast(error.status === 403 ? "AI ConnectionはMac本体の画面から設定してください。" : providerSetupFailureCopy().title);
	} finally {
		window.clearTimeout(timeout);
		setBusy(false);
	}
}

async function revealWorkspaceOnMac() {
	try {
		await requestJSON("/v1/local-setup/reveal-workspace", { method: "POST", body: "{}" });
		toast("MacのFinderに会社データを表示しました。Obsidianでは「Open folder as vault」を選んでください。");
	} catch (error) {
		toast(error.status === 403 ? "会社データを開く操作はMac本体で行ってください。" : "会社データをFinderに表示できませんでした。");
	}
}

function providerStatusCopy() {
  if (state.providerStatus?.configured) return {
    state: "Connected",
    className: "connected",
    description: "Claudeを利用できます。credentialやModel IDの値は画面へ表示しません。",
  };
  if (state.providerStatus?.invalid?.includes("status_unavailable")) return {
    state: "確認できません",
    className: "attention",
    description: "daemonとの接続を確認してから、状態を再確認してください。",
  };
  return {
    state: "Setup required",
    className: "attention",
    description: "AIサービスの接続が必要です。現在のPublic BetaではMacの起動設定を確認してください。",
  };
}

function renderProviderSettings() {
  const copy = providerStatusCopy();
  ui.providerSettings.replaceChildren(
    node("section", { class: `connection-card ${copy.className}` },
      node("div", { class: "connection-heading" },
        node("div", {}, node("strong", {}, "Claude"), node("small", {}, "AI service")),
        node("span", { class: `connection-state ${copy.className}` }, copy.state),
      ),
      node("p", {}, copy.description),
      !state.providerStatus?.configured ? node("p", { class: "connection-safety" }, "秘密情報はiPhoneやbrowser storageへ保存しません。接続設定はMac側で行います。") : null,
	  !state.providerStatus?.configured && state.localSetupAvailable ? button("MacでClaudeを接続", "primary", connectClaudeOnMac) : null,
	  !state.providerStatus?.configured && !state.localSetupAvailable ? node("p", { class: "connection-safety" }, "MacのWorkCairn画面でAI Connectionsを開いて接続してください。") : null,
    ),
	providerSetupFailureNode(),
  );
}

function providerSetupFailureNode() {
  if (!state.providerSetupError) return null;
  const copy = providerSetupFailureCopy();
  return node("section", { class: "error-box" },
	node("strong", {}, copy.title),
	node("p", {}, copy.message),
	node("details", {}, node("summary", {}, "技術的な詳細を見る"), approvalFacts([
	  ["Error code", state.providerSetupError.code], ["Stage", state.providerSetupError.stage],
	  ["Substage", state.providerSetupError.substage || "—"], ["Category", state.providerSetupError.category || "—"],
	])),
  );
}

function providerSetupFailureCopy() {
  if (state.providerSetupError?.category === "keychain_setup_timeout") return {
	title: "Claudeの接続設定を完了できませんでした",
	message: "安全な待機時間を超えたため処理を終了しました。Macの入力画面を閉じてから、もう一度お試しください。",
  };
  return {
	title: "Claude APIキーをMacのKeychainへ保存できませんでした",
	message: "自動retryや別の保存先へのfallbackは行っていません。MacのKeychain設定を確認してください。",
  };
}

function storageStatusCopy() {
  const kind = state.workspaceStatus?.storage_kind;
  if (kind === "icloud_drive") return ["iCloud DriveのWorkCairn専用Vault", "Obsidianから同じfolderをVaultとして開けます。同期中もcanonical metadataとCASを維持します。"];
  if (kind === "temporary") return ["Temporary Vault", "Acceptance専用です。通常利用では既存Vaultと分離したiCloud Drive / WorkCairnを選んでください。"];
  return ["WorkCairn専用のローカルVault", "既存の個人Obsidian Vaultは変更しません。後からこのfolderをObsidianで開けます。"];
}

function renderStorageSettings() {
  const [title, description] = storageStatusCopy();
  ui.storageSettings.replaceChildren(node("section", { class: "storage-card" },
	node("strong", {}, title), node("small", {}, description),
	state.localSetupAvailable ? button("Macで会社データを見る", "quiet", revealWorkspaceOnMac) : node("small", {}, "Obsidianで見る場合は、MacでこのWorkCairn専用folderをVaultとして開きます。"),
  ));
}

function openSettingsDialog() {
  renderProviderSettings();
  renderStorageSettings();
  ui.settingsDialog.showModal();
}

async function loadSessions() {
  state.sessions = await requestJSON("/v1/interactions");
  state.sessions.sort((left, right) => String(right.created_at).localeCompare(String(left.created_at)));
  renderSessions();
}

async function selectSession(id, options = {}) {
  if (!id) {
    state.record = null;
    state.next = null;
    state.workReport = null;
    state.workReportError = null;
    state.lastError = null;
    state.renderKey = "";
    state.detailRenderKey = "";
    state.timelineRenderKey = "";
    localStorage.removeItem(STORAGE_SESSION);
    renderEmpty();
    renderRequestDetail();
    renderEmployeesPane();
    applyNavigationLayout();
    return;
  }
  localStorage.setItem(STORAGE_SESSION, id);
  state.renderKey = "";
  state.detailRenderKey = "";
  state.timelineRenderKey = "";
  state.forceScrollToBottom = true;
  await refreshCurrent();
  if (options.openDetail !== false) {
    if (isDesktopLayout()) applyNavigationLayout();
    else showRequestDetail(id);
  }
}

async function refreshCurrent(silent = false) {
  const id = localStorage.getItem(STORAGE_SESSION);
  if (!id) {
    renderEmpty();
    return;
  }
  try {
    const [record, next, reportResult] = await Promise.all([
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}`),
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}/next`),
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}/work-report`)
        .then((report) => ({ report }))
        .catch((error) => ({ error })),
    ]);
    state.record = record;
    state.next = next;
    state.workReport = reportResult.report || null;
    state.workReportError = reportResult.error || null;
    restoreError(record);
    if (!state.lastError) await restoreDurableFailure(next);
    setConnected(true);
    renderRequestDetail();
    await loadSessions();
    await loadCompanyActivity().catch(() => null);
    renderEmployeesPane();
  } catch (error) {
    setConnected(false);
    showError(error, silent ? "Macとの接続を確認してください" : "依頼の状態を取得できませんでした");
  }
}

function renderEmpty() {
  if (isRequestDetailVisible()) {
    clearActionSurface();
    renderComposerState(null);
    ui.activeCard.hidden = false;
    ui.activeCard.replaceChildren(
      node("p", { class: "composer-note" }, "依頼した後はAI社員が計画・実行・レビューを進め、必要な質問と承認だけをここに表示します。"),
    );
    setQuickReplies([button("仕事を依頼する", "primary chip", openRequestDialog)]);
    renderTimeline();
  }
}

function renderSessions() {
  if (!state.sessions.length) {
    ui.sessionList.replaceChildren(node("p", { class: "empty" }, "まだ依頼はありません。"));
    return;
  }
  const activeID = state.record?.session_id;
  const groups = groupSessionsByDate(state.sessions);
  ui.sessionList.replaceChildren(...groups.flatMap((group) => [
    node("section", { class: "session-date-group" },
      node("h2", { class: "session-date-label" }, group.label),
      ...group.items.map((record) => {
        const presentation = sessionListPresentation(record);
        return node("button", {
          class: `session-item${activeID === record.session_id ? " active" : ""}`,
          type: "button",
          onclick: () => {
            selectSession(record.session_id);
            showRequestDetail(record.session_id);
          },
        },
        sessionIconNode(presentation.icon),
        node("span", { class: "session-copy" },
          node("span", { class: "session-title" }, record.request),
          node("span", { class: "session-meta" }, presentation.label),
        ),
        );
      }),
    ),
  ]));
}

function renderNext(force = false) {
  const next = state.next;
  if (!next) return renderEmpty();
  const pendingCommand = storedPendingCommand();
  const pendingForSession = pendingCommand && (!pendingCommand.payload?.session_id || pendingCommand.payload.session_id === next.session_id);
  const pendingInForeground = pendingForSession && isRequestDetailVisible();
  setBackgroundWorking(Boolean(pendingCommand) && !pendingInForeground);
  if (pendingForSession) {
    const key = `running:${pendingCommand.command_id}`;
    if (!force && state.renderKey === key) return;
    state.renderKey = key;
    return renderInFlight(pendingCommand);
  }
  const key = JSON.stringify([next.session_id, next.expected_version, next.kind, state.lastError?.code || "", state.lastError?.stage || ""]);
  if (!force && state.renderKey === key) return;
  state.renderKey = key;
  state.pendingAttentionTitle = "";
  if (next.kind !== "approve_workflow") state.workflowPlanPreview = null;
  if (next.kind !== "answer_clarifications") resetClarificationDraft();
  clearActionSurface();
  renderComposerState(next);
  if (state.lastError) return renderRememberedError(state.lastError);
  switch (next.kind) {
  case "approve_plan_generation": return renderPlanGeneration(next);
  case "answer_clarifications": return renderQuestions(next);
  case "approve_plan_apply": return renderPlanApproval(next);
  case "approve_workflow": return renderWorkflowApproval(next);
  case "inspect_workflow_recovery": return renderAttention(next, "Workflowの確認が必要です");
  case "optional_external_action_or_done": return renderCompletion(next);
  case "inspect_action_recovery": return renderAttention(next, "外部公開の確認が必要です");
  case "done": return renderDone();
  default: return showError(new Error(`Unsupported next action: ${next.kind}`));
  }
}

function renderRememberedError(error) {
  const facts = parseDiagnosticsFacts(error);
  setQuickReplies([
    button(error.command_id ? "Command状態を確認" : "状態を再確認", "primary chip", () => error.command_id
      ? inspectCommands([{ scope: "workspace", command_id: error.command_id }])
      : refreshCurrent()),
    button("新しい状態を確認", "quiet chip", async () => { clearCurrentError(); state.renderKey = ""; await refreshCurrent(); }),
  ]);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("p", { class: "composer-note" }, "エラーは消さずに保存しています。成立済みの仕事は保持し、自動retryや推測修復は行っていません。"),
    technicalDetails("技術的な詳細を見る", facts),
  );
  state.forceScrollToBottom = true;
  renderTimeline();
}

// restoreDurableFailure reconstructs the presentation from the Command
// references already persisted by an Interaction attention state. Browser
// storage remains a UX cache only: a fresh browser can recover the same safe
// FailureEnvelope projection from Ledger/server evidence after reload.
async function restoreDurableFailure(next) {
  if (!next || !["inspect_workflow_recovery", "inspect_action_recovery"].includes(next.kind) || !next.commands?.length) return;
  for (const reference of next.commands) {
    try {
      const query = new URLSearchParams({ scope: reference.scope });
      if (reference.project_name) query.set("project", reference.project_name);
      const record = await requestJSON(`/v1/commands/${encodeURIComponent(reference.command_id)}?${query}`);
      if (!record.failure || !["failed", "partial_failure"].includes(record.state)) continue;
      const diagnostics = errorDiagnostics(record.failure.details, record.result);
      rememberError(new APIError(record.failure.code, 422, {
        code: record.failure.code,
        stage: record.failure.stage,
        command_id: reference.command_id,
        recovery_required: record.state === "partial_failure",
        ...diagnostics,
      }), "前回のCommandを完了できませんでした", reference.command_id);
      return;
    } catch {
      // The existing attention screen still exposes an explicit read-only
      // inspection action. Never infer success or repair a missing record.
    }
  }
}

function renderPlanGeneration(next) {
  if (!state.providerStatus?.configured) return renderProviderSetup();
  setQuickReplies([
    button("進め方の作成を承認", "primary chip", () => executeNextCommand(next, {
      session_id: next.session_id,
      expected_version: next.expected_version,
      current_time: now(),
    }, "進め方を作成しています", "質問または仕事の進め方ができるまでお待ちください。")),
    button("今は承認しない", "quiet chip", () => toast("変更せず、承認待ちのまま保存されています。")),
  ]);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("p", { class: "composer-note" }, "AIはまだ会社データを変更しません。依頼内容を整理し、必要な質問と進め方を作成します。"),
  );
  state.forceScrollToBottom = true;
  renderTimeline();
}

function renderProviderSetup() {
  const missing = state.providerStatus?.missing || [];
  const invalid = state.providerStatus?.invalid || [];
  const reasons = [];
  if (missing.includes("credential")) reasons.push("Claudeがまだ接続されていません");
  if (invalid.length) reasons.push("Provider設定を安全に検証できませんでした");
  if (!reasons.length) reasons.push("接続状態を取得できませんでした");
  setQuickReplies([
    button("AI Connectionsを開く", "primary chip", openSettingsDialog),
    button("今は設定しない", "quiet chip", () => toast("依頼は進め方の作成待ちのまま保存されています。")),
  ]);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("ul", { class: "trust-list" }, ...reasons.map((reason) => node("li", {}, reason))),
    node("p", { class: "composer-note" }, "MacのAI ConnectionsからClaudeを接続してください。iPhoneからsecretは送らず、別Providerへの自動fallbackも行いません。"),
  );
  state.forceScrollToBottom = true;
  renderTimeline();
}

function renderQuestions(next) {
  const draft = clarificationDraft(next);
  const total = next.questions.length;
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("h2", { class: "visually-hidden" }, "確認したいことがあります"),
    total > 1 ? node("p", { class: "composer-note" }, `質問 ${draft.index + 1} / ${total}`) : null,
  );
  setQuickReplies([
    button("回答を送信", "primary chip", () => submitClarificationAnswers(next)),
    button("後で回答する", "quiet chip", () => toast("質問は回答待ちのまま保存されています。")),
  ]);
  state.forceScrollToBottom = true;
  renderTimeline();
}

function submitClarificationAnswers(next, answers = null) {
  if (answers) {
    if (answers.some((entry) => !entry.answer)) return toast("回答を入力してください。");
    executeNextCommand(next, {
      session_id: next.session_id,
      expected_version: next.expected_version,
      answers,
      current_time: now(),
    }, "回答を保存しています", "保存後に次の進め方の作成確認へ進みます。");
    resetClarificationDraft();
    ui.composerInput.value = "";
    return;
  }
  const draft = clarificationDraft(next);
  const answer = ui.composerInput.value.trim();
  if (!answer) return toast("回答を入力してください。");
  const currentQuestion = next.questions[draft.index];
  draft.answers.push({ question: currentQuestion, answer });
  ui.composerInput.value = "";
  if (draft.index + 1 < next.questions.length) {
    draft.index += 1;
    state.renderKey = "";
    state.timelineRenderKey = "";
    renderQuestions(next);
    renderComposerState(next);
    return;
  }
  executeNextCommand(next, {
    session_id: next.session_id,
    expected_version: next.expected_version,
    answers: draft.answers,
    current_time: now(),
  }, "回答を保存しています", "保存後に次の進め方の作成確認へ進みます。");
  resetClarificationDraft();
}

function currentPlan() {
  for (let index = state.record.turns.length - 1; index >= 0; index--) {
    const turn = state.record.turns[index];
    if (turn.kind === "plan_generated" && turn.plan) return { plan: turn.plan, digest: turn.plan_digest };
  }
  return null;
}

function roleLabel(role) {
  const labels = { "Product Manager": "企画担当", "Content Writer": "コンテンツ担当", "QA Engineer": "QA担当" };
  return labels[role] || "担当AI";
}

function planStepCopy(task, index) {
  return `${index + 1}. ${roleLabel(task.required_role)}が${task.title}`;
}

function renderPlanApproval(next) {
  const current = currentPlan();
  if (!current) return showError(new Error("Plan evidence is missing"));
  const identifier = localStorage.getItem(`workcairn.project.${state.record.session_id}`) || projectID();
  localStorage.setItem(`workcairn.project.${state.record.session_id}`, identifier);
  setQuickReplies([
    button("この進め方で始める", "primary chip", () => {
      executeNextCommand(next, {
        session_id: next.session_id, expected_version: next.expected_version,
        project_id: identifier, plan_digest: current.digest, current_time: now(),
      }, "Projectを作成しています", "ProjectとTaskを安全な順序で保存しています。");
    }),
    button("承認しない", "quiet chip", () => toast("Workspaceは変更されていません。")),
  ]);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("p", { class: "composer-note visually-hidden" }, "このように進めます"),
    technicalDetails("技術的な詳細を見る", [
      ["Project ID", identifier], ["Plan digest", shortDigest(current.digest)], ["Task数", String(current.plan.proposed_tasks.length)],
    ]),
  );
  state.forceScrollToBottom = true;
  renderTimeline();
}

async function loadOrganization(force = false) {
  if (force || !state.organization) state.organization = await requestJSON("/v1/organization");
  return state.organization;
}

async function renderWorkflowApproval(next) {
  state.workflowPlanPreview = null;
  const workflowFormKey = JSON.stringify([next.session_id, next.expected_version]);
  const currentForm = ui.activeCard.querySelector("form.stack-form[data-workflow-form-key]");
  if (currentForm?.dataset.workflowFormKey === workflowFormKey) return;
  const form = node("form", { class: "stack-form", dataset: { workflowFormKey } });
  const maxTasks = node("input", { id: "max-tasks", name: "max_tasks", type: "number", min: "1", max: "100", value: "20", inputmode: "numeric", required: true });
  form.append(
    node("p", { class: "empty" }, "Makerとは別のQA Reviewerを、役割と許可範囲から自動選択します。"),
    node("label", { for: "max-tasks" }, "今回任せる仕事ステップの上限"), maxTasks,
  );
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (form.dataset.submitting === "true") return;
    const limit = Number(maxTasks.value);
    if (!Number.isInteger(limit) || limit < 1 || limit > 100) return toast("1〜100のTask上限を確認してください。");
    form.dataset.submitting = "true";
    try {
      await prepareWorkflowApproval(next, limit);
    } finally {
      delete form.dataset.submitting;
    }
  });
  setQuickReplies([
    button("実行内容を確認", "primary chip", () => form.requestSubmit()),
    button("今は実行しない", "quiet chip", () => toast("仕事は開始されていません。")),
  ]);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(form);
  state.forceScrollToBottom = true;
  renderTimeline();
}

async function prepareWorkflowApproval(next, maxTasks) {
  const currentTime = now();
  setBusy(true, "実行内容を確認しています", "TaskとReviewerの現在状態をread-onlyで検証しています。");
  try {
    const plan = await requestJSON("/v1/interaction-workflow-plans", {
      method: "POST",
      body: JSON.stringify({ version: INTERACTION_VERSION, session_id: next.session_id, expected_version: next.expected_version, current_time: currentTime, max_tasks: maxTasks }),
    });
    setBusy(false);
    state.workflowPlanPreview = plan;
    setQuickReplies([
      button("承認して実行", "primary chip", () => executeNextCommand(next, {
        session_id: next.session_id, expected_version: next.expected_version,
        reviewer_id: plan.reviewer_id, current_time: currentTime, max_tasks: maxTasks,
        autonomy_contract: plan.autonomy_contract,
        workflow_plan_digest: plan.workflow_plan_digest,
        approval_reference: `mobile-ui:${next.session_id}:v${next.expected_version}`,
      }, "Workflowを実行しています", "Task、Review、必要なRevisionを順番に進めています。")),
      button("今は実行しない", "quiet chip", () => toast("仕事は開始されていません。")),
    ]);
    ui.activeCard.hidden = false;
    ui.activeCard.replaceChildren(
      node("p", { class: "composer-note" }, "承認後は担当AIが成果物を作り、別のReviewerが確認し、必要な修正まで上限内で進めます。"),
      approvalFacts([
        ["仕事", plan.project_name],
        ["レビュー担当", plan.reviewer_name],
        ["任せる上限", `${plan.autonomy_contract.execution_limit}件の仕事ステップ`],
        ["任せるAI社員", plan.autonomy_contract.allowed_employee_ids.map(employeeLabel).join(" / ")],
      ]),
      technicalDetails("技術的な詳細を見る", [
        ["Project ID", plan.project_id], ["Reviewer ID", plan.reviewer_id],
        ["Next Task", plan.next?.task_id || "readinessに従う"], ["Plan digest", plan.workflow_plan_digest],
      ]),
    );
    state.forceScrollToBottom = true;
    renderTimeline();
  } catch (error) {
    showError(error, "実行内容を確認できませんでした");
  }
}

function renderCompletion(next) {
  setQuickReplies([
    button("完了を確認", "primary chip", () => toast("この依頼は完了しています。")),
    button("新しい仕事を依頼", "quiet chip", openRequestDialog),
  ]);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("h2", { class: "visually-hidden" }, "すべての仕事とReviewが完了しています"),
    node("p", { class: "composer-note" }, "成果物はWorkspaceに保存されています。"),
  );
  renderComposerState(next);
  renderTimeline();
}

function renderDone() {
  const action = [...state.record.turns].reverse().find((turn) => turn.action)?.action;
  setQuickReplies([button("新しい仕事を依頼", "primary chip", openRequestDialog)]);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("p", { class: "composer-note" }, action?.publication?.url ? "成果物の外部公開まで完了しています。" : "成果物はWorkspaceに保存されています。"),
    action?.publication?.url ? node("a", { class: "button quiet chip-link", href: action.publication.url, target: "_blank", rel: "noreferrer" }, "公開先を開く") : null,
  );
  renderComposerState(state.next);
  renderTimeline();
}

function renderAttention(next, title) {
  const commandFacts = (next.commands || []).map((reference) => [
    reference.scope === "workspace" ? "Workspace command" : "Project command",
    reference.command_id,
  ]);
  setQuickReplies([
    button("Command状態を確認", "primary chip", () => inspectCommands(next.commands || [])),
    button("Sessionを再確認", "quiet chip", () => refreshCurrent()),
  ]);
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("p", { class: "composer-note" }, "成立済みの成果物や記録は保持されています。自動retryやrollbackをせず、CommandとRecovery evidenceを確認してください。"),
    commandFacts.length ? technicalDetails("Command references", commandFacts) : null,
  );
  state.pendingAttentionTitle = title;
  state.forceScrollToBottom = true;
  renderTimeline();
}

async function inspectCommands(references) {
  setBusy(true, "記録を確認しています", "Command Ledgerをread-onlyで取得しています。");
  try {
    const results = await Promise.all(references.map((reference) => {
      const query = new URLSearchParams({ scope: reference.scope });
      if (reference.project_name) query.set("project", reference.project_name);
      return requestJSON(`/v1/commands/${encodeURIComponent(reference.command_id)}?${query}`);
    }));
    const failures = results.flatMap((result, index) => {
      if (!result.failure) return [];
      const details = result.failure.details;
      return [
        ["Error code", result.failure.code],
        ["Stage", result.failure.stage || "—"],
        ["Substage", details?.substage || "—"],
        ["Category", details?.category || details?.provider?.category || "—"],
        ["HTTP status", details?.provider?.http_status || "—"],
        ["Request ID", details?.provider?.request_id || "—"],
        ["Command ID", references[index].command_id],
      ];
    });
    setBusy(false);
    setQuickReplies([button("閉じる", "quiet chip", () => renderNext(true))]);
    ui.activeCard.hidden = false;
    ui.activeCard.replaceChildren(
      node("h2", { class: "visually-hidden" }, "Command Ledger"),
      node("p", { class: "composer-note" }, "自動回復は行いません。partialまたはrunningの場合はMacのRecovery手順でcanonical evidenceを確認してください。"),
      approvalFacts(results.map((result, index) => [references[index].command_id, `${result.state}${result.failure?.stage ? ` / ${result.failure.stage}` : ""}`])),
      failures.length ? technicalDetails("技術的な詳細を見る", failures) : null,
    );
    state.pendingAttentionTitle = "Command Ledgerを確認してください。";
    renderTimeline();
  } catch (error) {
    showError(error, "Command状態を取得できませんでした");
  }
}

function closeActionDialog() { if (ui.actionDialog.open) ui.actionDialog.close(); }

function showApprovalSheet({ title, description, facts, technicalFacts = [], approveLabel, onApprove, approveKind = "primary", hideCancel = false }) {
  const form = ui.actionForm;
  form.replaceChildren(
    node("div", { class: "sheet-handle" }),
    node("div", { class: "sheet-heading" },
      node("div", {}, node("p", { class: "eyebrow" }, "CONFIRMATION"), node("h2", {}, title)),
      node("button", { class: "icon-button", type: "button", "aria-label": "閉じる", onclick: closeActionDialog }, "×"),
    ),
    node("p", { class: "supporting" }, description),
    approvalFacts(facts),
    technicalFacts.length ? node("details", { class: "technical-details" },
      node("summary", {}, "技術的な詳細を見る"), approvalFacts(technicalFacts),
    ) : null,
    node("div", { class: "sheet-actions" },
      hideCancel ? null : button("承認しない", "quiet", closeActionDialog),
      button(approveLabel, approveKind, null, "submit"),
    ),
  );
  form.onsubmit = async (event) => {
    event.preventDefault();
    closeActionDialog();
    try {
      await onApprove();
    } catch (error) {
      showError(error);
    }
  };
  ui.actionDialog.showModal();
}

function approvalFacts(facts) {
  return node("div", { class: "approval-box" }, node("dl", {}, ...facts.map(([term, value]) =>
    node("div", {}, node("dt", {}, term), node("dd", { class: String(value).startsWith("sha256:") ? "digest" : "" }, value || "—")),
  )));
}

async function executeNextCommand(next, payload, busyTitle, busyMessage, fixedCommandID = null) {
  if (state.commandInFlight || sessionStorage.getItem(STORAGE_PENDING)) {
    toast("同じ処理を実行中です。完了するまでお待ちください。");
    return false;
  }
  state.commandInFlight = true;
  let accepted = false;
  try {
    const command = {
      version: COMMAND_VERSION,
      command_id: fixedCommandID || commandID(),
      operation: next.operation,
      approved: true,
      payload,
    };
    state.activeCommandID = command.command_id;
    sessionStorage.setItem(STORAGE_PENDING, JSON.stringify(command));
    state.renderKey = "";
    renderInFlight(command);
    setBusy(false);
    setBackgroundWorking(false);
    await requestJSON("/v1/commands", {
      method: "POST",
      headers: { Prefer: "respond-async" },
      body: JSON.stringify(command),
    });
    accepted = true;
    await monitorAcceptedCommand(command);
    await refreshCurrent();
    setBusy(false);
    return true;
  } catch (error) {
    if (!accepted && error.status !== 0) sessionStorage.removeItem(STORAGE_PENDING);
    showError(error);
    return false;
  } finally {
    state.commandInFlight = Boolean(sessionStorage.getItem(STORAGE_PENDING));
  }
}

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function monitorAcceptedCommand(command) {
  setBusy(false);
  const viewingSession = localStorage.getItem(STORAGE_SESSION) === command.payload?.session_id && isRequestDetailVisible();
  setBackgroundWorking(!viewingSession);
  state.activeCommandID = command.command_id;
  if (viewingSession) {
    state.renderKey = "";
    renderInFlight(command);
  }
  renderEmployeesPane();
  let missing = 0;
  while (true) {
    let record;
    try {
      record = await requestJSON(`/v1/commands/${encodeURIComponent(command.command_id)}?scope=workspace`);
      missing = 0;
    } catch (error) {
      if (error.status !== 404 || ++missing >= 10) throw error;
      await wait(500);
      continue;
    }
    if (record.state === "succeeded") {
      sessionStorage.removeItem(STORAGE_PENDING);
      setBackgroundWorking(false);
      state.activeCommandID = "";
      state.commandInFlight = false;
      state.renderKey = "";
      return record;
    }
    if (record.state === "failed" || record.state === "partial_failure") {
      sessionStorage.removeItem(STORAGE_PENDING);
      setBackgroundWorking(false);
      state.commandInFlight = false;
      state.renderKey = "";
      throw new APIError(record.failure?.code || "COMMAND_FAILED", 422, {
        code: record.failure?.code || "COMMAND_FAILED",
        stage: record.failure?.stage,
        recovery_required: record.state === "partial_failure",
		command_id: command.command_id,
		...errorDiagnostics(record.failure?.details, record.result),
      });
    }
    if (record.state !== "running") {
	  setBackgroundWorking(false);
      throw new APIError("COMMAND_LEDGER_INVALID", 500, { code: "COMMAND_LEDGER_INVALID", recovery_required: true });
    }
    await wait(1000);
  }
}

// commandProviderFailure is the pre-Envelope legacy fallback: it tries
// several Result shapes to find a Provider diagnostic. Kept only for
// Commands not yet migrated to failure.Envelope propagation (Revision, CEO
// Plan, External Action) and for pre-migration Ledger records that never
// have a `details` field. Do not extend this function -- extend the
// server-side Envelope instead.
function commandProviderFailure(result) {
  if (result?.provider_failure) return result.provider_failure;
  for (const task of result?.workflow?.tasks || []) {
    if (task?.execution?.provider_failure) return task.execution.provider_failure;
    if (task?.review?.provider_failure) return task.review.provider_failure;
  }
  return null;
}

// errorDiagnostics prefers the single failure.Envelope (`details`) a
// migrated Command now carries, and falls back to the legacy multi-shape
// reconstruction (commandProviderFailure + top-level parse_failure_reason)
// only when `details` is absent -- pre-migration Ledger records, or
// Commands not yet migrated this round. showError()/rememberError() keep
// reading provider_failure/parse_failure_reason unchanged either way.
function errorDiagnostics(details, result) {
  return {
    details: details || null,
    provider_failure: details?.provider || commandProviderFailure(result),
    parse_failure_reason: details?.parse?.reason || result?.parse_failure_reason || null,
    parse_failure_field: details?.parse?.field || result?.parse_failure_field || null,
  };
}

function storedPendingCommand() {
  const serialized = sessionStorage.getItem(STORAGE_PENDING);
  if (!serialized) return null;
  try {
    const command = JSON.parse(serialized);
    if (command?.version !== COMMAND_VERSION || typeof command.command_id !== "string" || !command.command_id) return null;
    return command;
  } catch {
    return null;
  }
}

async function resumePendingCommand(serialized) {
  let command;
  try {
    command = JSON.parse(serialized);
  } catch {
    sessionStorage.removeItem(STORAGE_PENDING);
    return;
  }
  if (command?.version !== COMMAND_VERSION || typeof command.command_id !== "string" || !command.command_id) {
    sessionStorage.removeItem(STORAGE_PENDING);
    return;
  }
  state.commandInFlight = true;
  try {
    await monitorAcceptedCommand(command);
    await refreshCurrent();
    setBusy(false);
    toast("Macで完了した処理を反映しました。");
  } catch (error) {
    setBackgroundWorking(false);
    showError(error, "前回のCommand状態を確認できませんでした");
  } finally {
    state.commandInFlight = Boolean(sessionStorage.getItem(STORAGE_PENDING));
  }
}

function employeeIdentityByRole(role) {
  const employee = (state.organization?.inventory?.employees || []).find((candidate) => candidate.role === role);
  if (employee?.name || employee?.role) {
    return { name: employee.name || employee.role, role: employee.role ? roleLabel(employee.role) : "" };
  }
  if (role) return { name: "AI社員", role: roleLabel(role) };
  return { name: "AI社員", role: "" };
}

function employeeIdentityByID(id) {
  const employee = (state.organization?.inventory?.employees || []).find((candidate) => candidate.id === id);
  if (employee?.name || employee?.id) {
    return { name: employee.name || employee.id, role: employee.role ? roleLabel(employee.role) : "" };
  }
  if (id) return { name: "AI社員", role: "" };
  return { name: "AI社員", role: "" };
}

function speakerForRole(role) {
  return employeeIdentityByRole(role).name;
}

function reviewIssuesForTask(projectName, taskID) {
  const evidence = state.evidence.get(`${projectName}/${taskID}`);
  if (!evidence?.reviews?.length) return [];
  const latest = evidence.reviews[evidence.reviews.length - 1];
  return (latest.decision?.issues || []).map((issue) => issue.description).filter(Boolean);
}

function reviewMessageText(task, issues) {
  if (task.verdict === "Approve") {
    return task.targeted_revision
      ? "再確認しました。\n問題ありません。"
      : "内容を確認しました。\n問題ありません。";
  }
  const lines = ["修正をお願いします。"];
  if (issues.length) lines.push("", ...issues);
  return lines.join("\n");
}

function conversationNode(message) {
  const side = message.side || "system";
  const identity = message.identity || { name: message.speaker || "", role: message.role || "" };
  const actions = inlineMessageActions(message);

  if (side === "system") {
    const label = identity.name || message.speaker || "WorkCairn";
    const timeLabel = message.at ? sessionTimeLabel(message.at) : "";
    return node("article", { class: `msg msg-system${message.attention ? " attention" : ""}` },
      node("div", { class: "msg-system-rule", "aria-hidden": "true" }),
      node("p", { class: "msg-system-copy" }, node("strong", {}, label)),
      message.text || timeLabel ? node("div", { class: "msg-system-body" },
        message.text ? node("p", { class: "msg-system-copy" }, message.text) : null,
        timeLabel ? node("span", { class: "msg-system-time" }, ` · ${timeLabel}`) : null,
        actions,
      ) : actions,
      node("div", { class: "msg-system-rule", "aria-hidden": "true" }),
    );
  }

  const metaParts = [];
  if (identity.role && side === "employee") metaParts.push(node("span", { class: "msg-role" }, identity.role));
  if (message.at) metaParts.push(node("time", { class: "msg-time-inline" }, sessionTimeLabel(message.at)));

  const headerChildren = [
    node("span", { class: "msg-name" }, identity.name || message.speaker),
    metaParts.length ? node("div", { class: "msg-meta" }, ...metaParts) : null,
  ].filter(Boolean);
  const bodyClass = side === "user" ? "msg-body msg-body-user" : "msg-body msg-body-employee";

  return node("article", { class: `msg msg-${side}${message.liaison ? " msg-liaison" : ""}${message.attention ? " attention" : ""}` },
    node("div", { class: "msg-header" }, ...headerChildren),
    node("div", { class: `${bodyClass}${message.attention ? " attention" : ""}` },
      ...(message.text ? [node("p", { class: "msg-text" }, message.text)] : []),
      message.embed || null,
      actions,
    ),
  );
}

function planEmbedNode(plan) {
  if (!plan?.proposed_tasks?.length) return null;
  return node("div", { class: "msg-embed msg-embed-plan" },
    node("div", { class: "msg-attach" },
      node("p", { class: "msg-attach-label" }, "進め方"),
      node("ul", { class: "msg-attach-list" }, ...plan.proposed_tasks.map((task) => {
        const identity = employeeIdentityByRole(task.required_role);
        return node("li", {},
          node("strong", {}, identity.name),
          node("span", {}, task.title),
        );
      })),
    ),
  );
}

function deliverableEmbedNode(proof) {
  if (!proof?.deliverable?.committed && !proof?.title) return null;
  const label = proof.deliverable?.title || proof.title || "成果物";
  const preview = node("pre", { class: "deliverable-preview", hidden: true });
  const panel = node("div", { class: "msg-deliverable-panel", hidden: true }, preview);
  return node("div", { class: "msg-embed msg-embed-deliverable" },
    node("div", { class: "msg-attach" },
      node("p", { class: "msg-attach-label" }, "成果物"),
      node("p", { class: "msg-attach-preview" }, label),
      iconButton("成果物を見る", "⋯", async () => {
        panel.hidden = !panel.hidden;
        if (!panel.hidden && !preview.textContent) {
          await loadTaskEvidenceDetails();
          const workflow = [...(state.record?.turns || [])].reverse().find((turn) => turn.workflow)?.workflow;
          const evidence = workflow ? state.evidence.get(`${workflow.project_name}/${proof.task_id}`) : null;
          preview.textContent = evidence?.deliverable?.content || "（本文なし）";
        }
      }, "icon-button msg-more-toggle"),
      panel,
    ),
  );
}

function evidenceSystemMessages() {
  const messages = [];
  const proof = state.workReport?.proof_of_work;
  const workflow = [...(state.record?.turns || [])].reverse().find((turn) => turn.workflow)?.workflow;
  const workflowCompleted = workflow?.status === "completed" && !workflow?.failure;
  if (proof?.fully_verified && proof.tasks?.length && workflowCompleted) {
    const lines = proof.tasks
      .filter((task) => task.verified)
      .map((task) => {
        const title = task.title || task.task_id;
        if (task.review?.verdict === "Approve") return `・${title}（QAレビュー完了）`;
        return `・${title}`;
      });
    if (lines.length) {
      messages.push(workcairnEvent(`以下の仕事が完了しました。\n\n${lines.join("\n")}`));
    }
  }
  const contract = state.workReport?.autonomy_contract;
  if (contract && workflow) {
    const scopeLines = [
      `・最大${contract.execution_limit}件の仕事ステップ`,
      `・${contract.allowed_employee_ids.length}人のAI社員`,
      "・成果物は別のReviewerが確認",
    ];
    messages.push(workcairnEvent(`今回はこの範囲を任せて進めました。\n\n${scopeLines.join("\n")}`));
  }
  const attention = state.workReport?.ceo_attention;
  if (attention && (attention.delegated_steps > 0 || attention.company_steps > 0)) {
    const lines = [];
    if (attention.company_steps > 0) lines.push(`・会社が進めたstep: ${attention.company_steps}`);
    if (attention.delegated_steps > 0) lines.push(`・呼ばずに進めたstep: ${attention.delegated_steps}`);
    if (attention.clarification_questions > 0) lines.push(`・必要だった質問: ${attention.clarification_questions}`);
    if (attention.approval_moments > 0) lines.push(`・重要な承認: ${attention.approval_moments}`);
    if (lines.length) messages.push(workcairnEvent(`任せて進んだ仕事\n\n${lines.join("\n")}`));
  }
  return messages;
}

function pendingInteractionMessages() {
  const next = state.next;
  if (!next) return [];
  const pending = storedPendingCommand();
  if (pending) {
    const copy = inFlightCopy(pending.operation);
    return [liaisonMessage(copy.label, now(), {
      detail: [["Command ID", pending.command_id || "確認中"]],
    })];
  }
  if (state.lastError) {
    return [liaisonMessage(`${state.lastError.title || "処理を完了できませんでした。"} 自動retryせず、次の判断を待っています。`, state.lastError.at, {
      attention: true,
      detail: parseDiagnosticsFacts(state.lastError),
      onCopy: () => copySanitizedError(state.lastError),
    })];
  }
  if (state.pendingAttentionTitle) {
    return [liaisonMessage(state.pendingAttentionTitle, null, { attention: true })];
  }
  switch (next.kind) {
  case "approve_plan_generation": {
    if (!state.providerStatus?.configured) {
      return [liaisonMessage("AIサービスへ接続してください。", null, { attention: true })];
    }
    const hasAnswers = state.record?.turns?.some((turn) => turn.kind === "clarification_answered");
    return [liaisonMessage(hasAnswers ? "回答をもとに進め方を作り直します。" : "仕事の進め方を作成します。")];
  }
  case "answer_clarifications": {
    const draft = clarificationDraft(next);
    const messages = [];
    for (const entry of draft.answers) {
      messages.push(liaisonMessage(entry.question));
      messages.push({ side: "user", speaker: "あなた", text: entry.answer });
    }
    const current = next.questions[draft.index];
    if (current) messages.push(liaisonMessage(current));
    return messages;
  }
  case "approve_plan_apply": {
    const current = currentPlan();
    return [liaisonMessage("この進め方で開始しますか？", null, { embed: current ? planEmbedNode(current.plan) : null })];
  }
  case "approve_workflow":
    if (state.workflowPlanPreview) {
      return [liaisonMessage("この進め方で開始しますか？")];
    }
    return [liaisonMessage("担当AIに仕事を開始させます。Reviewerの指摘があれば修正と再確認まで進めます。")];
  case "inspect_workflow_recovery":
    return [liaisonMessage("Workflowの確認が必要です", null, { attention: true })];
  case "inspect_action_recovery":
    return [liaisonMessage("外部公開の確認が必要です", null, { attention: true })];
  default:
    return [];
  }
}

function conversationMessages() {
  const record = state.record;
  if (!record) return [];
  const proofByTask = new Map((state.workReport?.proof_of_work?.tasks || []).map((task) => [task.task_id, task]));
  const messages = [{
    side: "user",
    speaker: "あなた",
    text: record.request,
    at: record.created_at,
  }];
  for (let turnIndex = 0; turnIndex < (record.turns || []).length; turnIndex++) {
    const turn = record.turns[turnIndex];
    if (turn.kind === "clarification_answered" && turn.answers?.length) {
      for (const answer of turn.answers) {
        messages.push(liaisonMessage(answer.question, turn.at));
        messages.push({ side: "user", speaker: "あなた", text: answer.answer, at: turn.at });
      }
    }
    if (turn.kind === "plan_generated" && turn.plan) {
      messages.push(liaisonMessage("進め方をまとめました。", turn.at, {
        embed: planEmbedNode(turn.plan),
        detail: [`Plan digest: ${turn.plan_digest}`, ...turn.plan.proposed_tasks.map((task, index) => `${index + 1}. ${planStepCopy(task, index).replace(/^\d+\. /, "")}`)].join("\n"),
      }));
    } else if (turn.kind === "plan_applied") {
      const planTurn = [...record.turns.slice(0, turnIndex)].reverse().find((candidate) => candidate.kind === "plan_generated" && candidate.plan);
      if (planTurn?.plan?.proposed_tasks?.length) {
        const assignments = planTurn.plan.proposed_tasks.map((task) => {
          const assignee = employeeIdentityByRole(task.required_role);
          return `${assignee.name}に${task.title}`;
        });
        messages.push(liaisonMessage(`この内容で進めます。\n\n${assignments.join("\n")}`, turn.at, {
          detail: `Project: ${turn.project_name}\nProject ID: ${turn.project_id}`,
        }));
      } else {
        messages.push(workcairnEvent(`${turn.project_name}の準備が整いました。`, turn.at, {
          detail: `Project: ${turn.project_name}\nProject ID: ${turn.project_id}`,
        }));
      }
    } else if (turn.workflow) {
      const projectName = turn.workflow.project_name || "";
      const tasks = turn.workflow.tasks || [];
      for (const task of tasks) {
        const proof = proofByTask.get(task.task_id);
        const maker = employeeIdentityByID(proof?.maker_id);
        if (!task.targeted_revision && task.execution_command_id) {
          messages.push(workcairnEvent(`${maker.name}が作業を開始しました。`, turn.at, {
            detail: [`Task: ${task.task_id}`, task.execution_command_id ? `Execution Command: ${task.execution_command_id}` : ""].filter(Boolean).join("\n"),
          }));
        }
        if (task.targeted_revision) {
          messages.push(workcairnEvent(`${maker.name}が修正しました。`, turn.at, {
            detail: task.revision_task_id ? `Revision Task: ${task.revision_task_id}` : "",
          }));
        }
        if (task.verdict === "Request Changes") {
          const issues = reviewIssuesForTask(projectName, task.task_id);
          messages.push(liaisonMessage(
            issues.length ? `レビューで修正をお願いされています。\n\n${issues.join("\n")}` : "レビューで修正をお願いされています。",
            turn.at,
            { attention: true, detail: [`Review: ${task.verdict}`, task.review_command_id ? `Review Command: ${task.review_command_id}` : ""].filter(Boolean).join("\n") },
          ));
        }
      }
      if (turn.workflow.status === "completed" && !turn.workflow.failure) {
        const primaryTask = tasks.find((task) => !task.targeted_revision) || tasks[0];
        const proof = primaryTask ? proofByTask.get(primaryTask.task_id) : null;
        const reviewer = employeeIdentityByID(proof?.review?.reviewer_id);
        const taskTitle = proof?.title || proof?.deliverable?.title || "原稿";
        const reviewLine = reviewer.name !== "AI社員" ? `${reviewer.name}のレビューも完了しています。` : "レビューも完了しています。";
        messages.push(liaisonMessage(`${taskTitle}が完成しました。\n${reviewLine}`, turn.at, {
          embed: deliverableEmbedNode(proof),
        }));
        messages.push(liaisonMessage("完了しました。", turn.at));
      }
      if (turn.workflow.failure) {
        messages.push(liaisonMessage("処理を完了できませんでした。", turn.at, {
          attention: true,
          detail: `${turn.workflow.failure.code} / ${turn.workflow.failure.stage}`,
        }));
      }
    } else if (turn.action) {
      messages.push(workcairnEvent(
        turn.action.status === "published" ? "外部公開が完了しました。" : "外部Actionを停止しました。",
        turn.at,
        { attention: turn.action.status !== "published", detail: turn.action.command_id || "" },
      ));
    }
  }
  messages.push(...evidenceSystemMessages());
  messages.push(...pendingInteractionMessages());
  return messages;
}

function renderTimeline() {
  const messages = conversationMessages();
  const key = JSON.stringify(messages.map((message) => [message.side, message.speaker, message.text, message.at, message.attention]));
  const shouldFollow = state.forceScrollToBottom || state.threadNearBottom;
  if (state.timelineRenderKey === key) {
    if (state.forceScrollToBottom) {
      requestAnimationFrame(() => scrollThreadToBottom());
      state.forceScrollToBottom = false;
    }
    return;
  }
  state.timelineRenderKey = key;
  ui.timeline.replaceChildren(...(messages.length ? messages.map(conversationNode) : [node("p", { class: "empty" }, "依頼すると、会社の動きがここに残ります。")]));
  if (shouldFollow) {
    requestAnimationFrame(() => {
      scrollThreadToBottom();
      state.forceScrollToBottom = false;
    });
  } else {
    showJumpToLatest(true);
  }
}

function renderDetails() {
  const record = state.record;
  if (!record) {
    ui.detailsPanel.hidden = true;
    ui.details.replaceChildren();
    state.detailRenderKey = "";
    return;
  }
  const evidenceKey = [...state.evidence.entries()].map(([evidenceID, evidence]) => [
    evidenceID, evidence?.task?.version || 0, evidence?.deliverable?.relative_path || "", (evidence?.reviews || []).length,
  ]);
  const key = JSON.stringify([record.session_id, record.version, evidenceKey]);
  if (state.detailRenderKey === key) return;
  state.detailRenderKey = key;
  ui.detailsPanel.hidden = false;
  const blocks = [
    detailBlock("依頼", [record.request, `Session: ${record.session_id}`, `Version: ${record.version} / ${stateLabel(record.state)}`]),
  ];
  const current = currentPlan();
  if (current) {
    blocks.push(detailBlock("進め方（詳細）", [
      current.plan.objective,
      current.plan.summary,
      ...current.plan.proposed_tasks.map((task) => `${task.proposal_id}: ${task.title}${task.assignee_id ? ` — ${task.assignee_id}` : ""}`),
    ]));
  }
  const applied = [...record.turns].reverse().find((turn) => turn.kind === "plan_applied");
  if (applied) blocks.push(detailBlock("Project", [`${applied.project_name} (${applied.project_id})`]));
  const workflow = [...record.turns].reverse().find((turn) => turn.workflow)?.workflow;
  if (workflow) {
    blocks.push(detailBlock("Task・Review・Revision", [
      `状態: ${workflow.status}`,
      ...workflow.tasks.map((task) => `${task.task_id}: Review ${task.verdict || "未完了"}${task.revision_task_id ? ` / Revision ${task.revision_task_id}` : ""}`),
      workflow.failure ? `要確認: ${workflow.failure.code} / ${workflow.failure.stage}` : null,
    ].filter(Boolean)));
    for (const task of workflow.tasks) {
      const key = `${workflow.project_name}/${task.task_id}`;
      const evidence = state.evidence.get(key);
      if (evidence) blocks.push(taskEvidenceBlock(evidence));
    }
  }
  const action = [...record.turns].reverse().find((turn) => turn.action)?.action;
  if (action) {
    blocks.push(detailBlock("External Action", [
      `状態: ${action.status}`, `Task: ${action.task_id}`, `公開先: ${action.target_id}`,
      action.publication?.url ? `URL: ${action.publication.url}` : null,
      action.failure ? `要確認: ${action.failure.code} / ${action.failure.stage}` : null,
    ].filter(Boolean)));
  }
  ui.details.replaceChildren(...blocks);
}

function taskEvidenceBlock(evidence) {
  const children = [
    node("h3", {}, `成果物・Review — ${evidence.task.id}`),
    node("p", { class: "supporting" }, `Task状態: ${evidence.task.status} / Version ${evidence.task.version}`),
  ];
  if (evidence.deliverable) {
    children.push(node("details", { class: "artifact-detail" },
      node("summary", {}, evidence.deliverable.title || evidence.deliverable.relative_path),
      node("dl", { class: "fact-grid" },
        node("div", {}, node("dt", {}, "担当"), node("dd", {}, evidence.deliverable.assignee_id)),
        node("div", {}, node("dt", {}, "実行時刻"), node("dd", {}, evidence.deliverable.executed_at)),
        node("div", {}, node("dt", {}, "保存先"), node("dd", {}, evidence.deliverable.relative_path)),
      ),
      node("pre", { class: "deliverable-preview" }, evidence.deliverable.content || "（本文なし）"),
    ));
  } else {
    children.push(node("p", { class: "warning" }, "Deliverableはまだcommitされていません。"));
  }
  for (const review of evidence.reviews || []) {
    children.push(node("div", { class: "review-summary" },
      node("strong", {}, `Review: ${review.decision.verdict}`),
      node("small", {}, review.canonical_path),
      ...(review.decision.issues || []).map((issue) => node("p", {}, `${issue.severity}: ${issue.description}`)),
    ));
  }
  return node("section", { class: "detail-block" }, ...children);
}

async function loadTaskEvidenceDetails() {
  const workflow = [...(state.record?.turns || [])].reverse().find((turn) => turn.workflow)?.workflow;
  if (!workflow) return;
  const missing = workflow.tasks.filter((task) => !state.evidence.has(`${workflow.project_name}/${task.task_id}`));
  if (!missing.length) return;
  try {
    const results = await Promise.all(missing.map((task) => requestJSON(`/v1/projects/${encodeURIComponent(workflow.project_name)}/tasks/${encodeURIComponent(task.task_id)}/evidence`)));
    results.forEach((evidence, index) => state.evidence.set(`${workflow.project_name}/${missing[index].task_id}`, evidence));
    renderDetails();
    renderRequestDetail();
  } catch (error) {
    toast(`成果物の詳細を取得できませんでした: ${error.message}`);
  }
}

function renderRequestDetail() {
  renderRequestSummary();
  if (!state.record) {
    ui.requestSummary.replaceChildren();
    ui.activeCard.replaceChildren();
    ui.timeline.replaceChildren();
    ui.detailsPanel.hidden = true;
    ui.details.replaceChildren();
    renderAutonomy();
    renderProofOfWork();
    renderCEOAttention();
    applyNavigationLayout();
    return;
  }
  applyNavigationLayout();
  renderNext();
  renderDetails();
  renderTimeline();
  renderAutonomy();
  renderProofOfWork();
  renderCEOAttention();
  renderComposerState(state.next);
}

function renderRequestSummary() {
  const record = state.record;
  if (!record) {
    ui.requestSummary.replaceChildren();
    return;
  }
  ui.requestSummary.replaceChildren(
    node("h1", { class: "thread-title" }, record.request),
  );
}

async function loadCompanyActivity(force = false) {
  if (!force && state.companyActivity) return state.companyActivity;
  state.companyActivity = await requestJSON("/v1/company-activity");
  return state.companyActivity;
}

async function refreshEmployeesPane() {
  try {
    await Promise.all([loadOrganization(), loadCompanyActivity(true)]);
    await loadTaskEvidenceDetails();
    renderEmployeesPane();
  } catch {
    ui.employeeGrid.replaceChildren(node("p", { class: "warning" }, "社員情報を読み込めませんでした。仕事の状態は推測せず、Mac側のOrganizationを確認してください。"));
  }
}

function renderEmployeesPane() {
  const employees = state.companyActivity?.employees || [];
  ui.teamCount.textContent = employees.length ? `${employees.length} people` : "";
  if (!employees.length) {
    ui.employeeGrid.replaceChildren(node("p", { class: "empty" }, "AI社員はまだ読み込まれていません。"));
  } else {
    ui.employeeGrid.replaceChildren(...employees.map((employee) => {
      const statusClass = employeeStatusClass(employee.display_status);
      return node("article", { class: `employee-card ${statusClass}${employee.is_liaison ? " liaison" : ""}`.trim() },
        node("span", { class: "employee-avatar", "aria-hidden": "true" }),
        node("div", { class: "employee-name" },
          node("strong", {}, employee.name || employee.id),
          node("small", {}, `${employee.role || "役割未設定"} · ${employee.department || "所属未設定"}`),
          node("span", { class: `employee-status ${statusClass}`.trim() },
            node("span", { class: "status-dot", "aria-hidden": "true" }),
            employee.display_status || "待機中",
          ),
          employee.current_work_title ? node("p", { class: "employee-task" }, employee.current_work_title) : null,
        ),
      );
    }));
  }
  renderCompanyFeed();
}

function renderCompanyFeed() {
  const events = state.companyActivity?.feed || [];
  if (!events.length) {
    ui.companyFeed.replaceChildren(node("p", { class: "empty" }, "社内の動きはここに表示されます。"));
    return;
  }
  ui.companyFeed.replaceChildren(...events.slice().reverse().map((event) => {
    const time = event.at ? sessionTimeLabel(event.at) : "";
    const directed = Boolean(event.directed && event.target_name);
    const route = directed
      ? node("p", { class: "feed-copy" }, node("strong", {}, event.actor_name), ` → ${event.target_name}`, node("span", {}, ` ${event.label}`))
      : node("p", { class: "feed-copy" }, node("strong", {}, event.actor_name), node("span", {}, ` ${event.label}`));
    return node("article", { class: "feed-item" },
      node("time", { class: "feed-time" }, time),
      route,
    );
  }));
}

function employeeStatusClass(displayStatus) {
  switch (displayStatus) {
  case "作業中": return "working";
  case "レビュー中": return "reviewing";
  case "修正中": return "revising";
  case "完了": return "completed";
  case "社長と相談中": return "with-ceo";
  default: return "standby";
  }
}

function openSetupWizard() {
  renderSetupWizard();
  if (!ui.setupDialog.open) ui.setupDialog.showModal();
}

function renderSetupWizard() {
  const workspace = state.workspaceStatus;
  if (!workspace) {
    ui.setupContent.replaceChildren(node("p", { class: "warning" }, "Mac側の会社データ状態を確認できません。daemonの接続を確認してください。"));
    return;
  }
  const storage = storageStatusCopy();
  const missing = new Set(workspace.missing_roles || []);
  const people = (workspace.starter_organization || []).map((candidate) =>
    node("div", { class: "setup-person" }, node("span", {}, candidate.role), node("strong", {}, missing.has(candidate.role) ? "追加が必要" : "準備済み")),
  );
  ui.setupContent.replaceChildren(
    node("article", { class: `setup-step ${workspace.layout_ready ? "ready" : "attention"}` },
      node("div", { class: "setup-step-heading" }, node("h3", {}, "1. 会社データの保存場所"), node("span", { class: "state-chip" }, workspace.layout_ready ? "準備済み" : "選択済み")),
      node("p", {}, storage[0]), node("p", {}, storage[1]),
      !workspace.layout_ready ? node("p", { class: "warning" }, "保存先は選択済みです。Starter Organizationの承認後、この専用directoryの中だけに会社データを準備します。") : null,
      workspace.storage_kind === "icloud_drive" ? node("p", { class: "warning" }, "同じVaultへ書き込むWorkCairn daemonは1台だけにしてください。iCloud同期を複数writerの調停には使いません。") : null,
    ),
    node("article", { class: `setup-step ${workspace.organization_ready ? "ready" : "attention"}` },
	  node("div", { class: "setup-step-heading" }, node("h3", {}, "2. 最初のAIチーム"), node("span", { class: "state-chip" }, workspace.organization_ready ? "準備済み" : "承認が必要")),
      node("p", {}, "企画・作成・独立Reviewができる最小チームです。既存社員は変更しません。"),
      node("div", { class: "setup-people" }, ...people),
      !workspace.organization_ready ? node("p", { class: "warning" }, "社員の追加は会社データへの変更です。下の確認画面で明示承認した場合だけ、この専用Vaultへ追加します。秘密情報はbrowserへ保存しません。") : null,
    ),
    node("article", { class: `setup-step ${state.providerStatus?.configured ? "ready" : "attention"}` },
      node("div", { class: "setup-step-heading" }, node("h3", {}, "3. AI Connection"), node("span", { class: "state-chip" }, state.providerStatus?.configured ? "Connected" : "Setup required")),
	  node("p", {}, "RoutingはAutomaticです。Model IDを選ぶ必要はありません。credentialはbrowserへ保存しません。"),
	  !state.providerStatus?.configured && state.localSetupAvailable ? button("MacでClaudeを接続", "quiet", connectClaudeOnMac) : null,
	  !state.providerStatus?.configured && !state.localSetupAvailable ? node("p", { class: "warning" }, "AI ConnectionはMac本体のWorkCairn画面から設定してください。") : null,
    ),
	providerSetupFailureNode(),
    node("div", { class: "setup-actions" },
	  !workspace.organization_ready ? button("最初のAIチームを確認", "primary", () => renderSetupTeamApproval(workspace)) : null,
      !state.providerStatus?.configured ? button("AI Connectionsを確認", "primary", () => { ui.setupDialog.close(); openSettingsDialog(); }) : null,
	  workspace.organization_ready && state.providerStatus?.configured ? button("会社を始める", "primary", () => { ui.setupDialog.close(); setNav("request_list"); focusNewRequestForm(); }) : null,
	  workspace.layout_ready && state.localSetupAvailable ? button("Obsidianで会社データを見る", "quiet", revealWorkspaceOnMac) : null,
      button("Macで設定してから再確認", "quiet", async () => { await Promise.all([loadWorkspaceStatus(), loadProviderStatus(), loadOrganization().catch(() => null)]); renderSetupWizard(); }),
    ),
  );
}

function renderCEOAttention() {
  const attention = state.workReport?.ceo_attention;
  if (!attention) {
    ui.attentionGrid.replaceChildren(node("p", { class: "empty" }, state.record ? "仕事が進むと、任せられたstepをここに表示します。" : "依頼後に表示されます。"));
    return;
  }
  const metrics = [
    [attention.company_steps, "会社が進めたstep"],
    [attention.delegated_steps, "呼ばずに進めたstep"],
    [attention.clarification_questions, "必要だった質問"],
    [attention.approval_moments, "重要な承認"],
  ];
  if (attention.recovery_attention_required) metrics.push([attention.recovery_attention_required, "復旧の確認"]);
  ui.attentionGrid.replaceChildren(...metrics.map(([value, label]) =>
    node("div", { class: "attention-stat" }, node("strong", {}, String(value)), node("span", {}, label)),
  ));
}

function renderAutonomy() {
  const contract = state.workReport?.autonomy_contract;
  if (!contract) {
    ui.autonomySummary.replaceChildren(node("p", { class: "empty" }, "実行承認時に、今回任せる範囲を固定します。"));
    return;
  }
  ui.autonomySummary.replaceChildren(
    node("p", { class: "insight-lead" }, `最大${contract.execution_limit}件の仕事ステップを、${contract.allowed_employee_ids.length}人のAI社員に任せています。`),
    node("ul", { class: "trust-list" },
      node("li", {}, "成果物は必ず別のReviewerが確認"),
      node("li", {}, "修正は同じ範囲で自動継続"),
      node("li", {}, "外部公開は毎回あなたの承認が必要"),
      node("li", {}, "支出は許可していません"),
    ),
  );
}

function renderProofOfWork() {
  const report = state.workReport;
  if (state.workReportError) {
    ui.proofOfWork.replaceChildren(node("p", { class: "warning" }, "仕事の記録を確認できません。完了を推測せず、Mac側の状態を確認してください。"));
    return;
  }
  const proof = report?.proof_of_work;
  if (!proof?.tasks?.length) {
    ui.proofOfWork.replaceChildren(node("p", { class: "empty" }, "成果物とReviewが成立すると、ここから辿れます。"));
    return;
  }
  const tasks = proof.tasks.map((task) => node("article", { class: `proof-card${task.verified ? " verified" : " attention"}` },
    node("div", { class: "proof-heading" },
      node("strong", {}, task.title || task.task_id),
      node("span", { class: "proof-state" }, task.verified ? "確認済み" : "確認が必要"),
    ),
    node("p", {}, `${employeeLabel(task.maker_id)} が作成 · ${employeeLabel(task.review.reviewer_id)} がReview`),
    node("ul", { class: "proof-facts" },
      node("li", {}, task.deliverable.committed ? "成果物を保存済み" : "成果物の保存を未確認"),
      node("li", {}, task.review.canonical_committed ? `Review: ${task.review.verdict}` : "Review記録を未確認"),
      task.revision.occurred ? node("li", {}, task.revision.intent_committed ? "Request Changesを受け、修正を記録済み" : "修正の記録を要確認") : null,
    ),
  ));
  if (proof.external_action.requested) {
    tasks.push(node("article", { class: `proof-card${proof.external_action.verified ? " verified" : " attention"}` },
      node("div", { class: "proof-heading" }, node("strong", {}, "外部Action"), node("span", { class: "proof-state" }, proof.external_action.verified ? "成立済み" : "確認が必要")),
      node("p", {}, proof.external_action.verified ? `公開先 ${proof.external_action.target_id} への反映を確認しました。` : "成立済み記録を保持したまま、自動継続を停止しています。"),
    ));
  }
  ui.proofOfWork.replaceChildren(
    node("p", { class: "insight-lead" }, proof.fully_verified ? `${proof.verified_tasks}件の仕事を保存済みの確定記録で確認しました。` : "完了を推測せず、成立済みの記録だけを表示しています。"),
    proof.audit?.readable && proof.audit.recorded_events > 0
      ? node("p", { class: "audit-note" }, `監査記録 ${proof.audit.recorded_events}件を確認`)
      : node("p", { class: "warning" }, "監査記録を確認できないため、完了として断定しません。"),
    ...tasks,
  );
}

function employeeLabel(id) {
  if (!id) return "未割当";
  const employee = (state.organization?.inventory?.employees || []).find((candidate) => candidate.id === id);
  return employee ? (employee.name || employee.id) : id;
}

function detailBlock(title, rows) {
  return node("section", { class: "detail-block" }, node("h3", {}, title), node("ul", {}, ...rows.map((row) => node("li", {}, row))));
}

function renderSetupTeamApproval(workspace) {
  ui.setupContent.replaceChildren(
    node("article", { class: "setup-step attention" },
      node("h3", {}, "最小のAIチームを作成しますか？"),
      node("p", {}, "承認すると、このWorkCairn専用Vaultだけに企画・コンテンツ・QA担当を追加します。既存社員や個人Vaultは変更しません。"),
      node("div", { class: "setup-people" }, ...(workspace.starter_organization || []).map((candidate) =>
        node("div", { class: "setup-person" }, node("span", {}, candidate.role), node("strong", {}, candidate.name)),
      )),
      node("div", { class: "setup-actions" },
        button("承認してセットアップ", "primary", async () => {
          const completed = await executeNextCommand({ operation: "workspace.setup" }, { current_time: now() }, "会社を準備しています", "専用VaultへStarter Organizationを安全に作成しています。", commandID());
          if (!completed) return;
          await Promise.all([loadWorkspaceStatus(), loadOrganization(true), loadCompanyActivity(true)]);
          renderEmployeesPane();
          if (!state.providerStatus?.configured) openSettingsDialog();
          else openSetupWizard();
        }),
        button("戻る", "quiet", () => renderSetupWizard()),
      ),
    ),
  );
}

function focusNewRequestForm() {
  showRequestList();
  const field = document.querySelector("#request-text");
  if (field) {
    field.focus();
    field.scrollIntoView({ block: "nearest" });
  }
}

function restoreNewRequestForm() {
  const inline = document.querySelector("#new-request-inline");
  if (!inline) return;
  inline.replaceChildren(
    node("form", { id: "request-form", class: "stack-form" },
      node("label", { for: "request-text" }, "会社に任せたい仕事"),
      node("textarea", { id: "request-text", name: "request", rows: "3", placeholder: "例：新商品の紹介記事を企画して、レビューまで完了してください", required: true }),
      node("div", { class: "button-row" },
        button(liaisonRequestLabel(), "primary", null, "submit"),
      ),
    ),
  );
  ui.requestForm = document.querySelector("#request-form");
  ui.requestForm.addEventListener("submit", prepareNewRequest);
}

function openRequestDialog() {
  focusNewRequestForm();
}

async function prepareNewRequest(event) {
  event.preventDefault();
  try {
    const data = new FormData(ui.requestForm);
    const request = data.get("request")?.toString().trim();
    if (!request) return toast("依頼内容を入力してください。");
    const input = { version: INTERACTION_VERSION, session_id: sessionID(), request, current_time: now() };
    setBusy(true, "依頼内容を確認しています", "まだWorkspaceやProviderは変更しません。");
    const plan = await requestJSON("/v1/interaction-plans", { method: "POST", body: JSON.stringify(input) });
    setBusy(false);
    state.pendingStart = { input, plan, request };
    const inline = document.querySelector("#new-request-inline");
    inline.replaceChildren(
      conversationNode(liaisonMessage("この依頼を開始してよろしいですか？", now())),
      node("p", { class: "composer-note" }, request),
      technicalDetails("技術的な詳細を見る", [["Request digest", plan.session.request_digest]]),
      node("div", { class: "inline-actions button-row" },
        button("依頼を開始", "primary", async () => {
          localStorage.setItem(STORAGE_SESSION, input.session_id);
          const completed = await executeNextCommand({ operation: "interaction.start" }, {
            session_id: input.session_id, request, request_digest: plan.session.request_digest,
            model: plan.session.model, current_time: input.current_time,
          }, "依頼を保存しています", "Sessionを作成しています。", commandID());
          state.pendingStart = null;
          restoreNewRequestForm();
          if (completed) showRequestDetail(input.session_id);
        }),
        button("やめる", "quiet", () => { state.pendingStart = null; restoreNewRequestForm(); }),
      ),
    );
  } catch (error) {
    showError(error, "依頼内容を確認できませんでした");
  }
}

function closeDialog(event) {
  const dialog = event.currentTarget.closest("dialog");
  if (dialog?.open) dialog.close();
}

ui.pairingForm.addEventListener("submit", pair);
ui.requestForm.addEventListener("submit", prepareNewRequest);
document.querySelector("#refresh-button").addEventListener("click", () => refreshCurrent());
ui.settingsButton.addEventListener("click", openSettingsDialog);
document.querySelector("#provider-status-refresh").addEventListener("click", refreshProviderStatus);
ui.menuButton.addEventListener("click", openNavDrawer);
ui.navBackdrop.addEventListener("click", closeNavDrawer);
ui.navEmployeesHome.addEventListener("click", showEmployeesHome);
ui.navRequestList.addEventListener("click", showRequestList);
ui.navNewRequest.addEventListener("click", () => { closeNavDrawer(); focusNewRequestForm(); });
ui.navCurrentRequest.addEventListener("click", () => showRequestDetail());
ui.navSettings.addEventListener("click", () => { closeNavDrawer(); openSettingsDialog(); });
ui.backToListButton.addEventListener("click", showRequestList);
if (ui.threadScroll) {
  ui.threadScroll.addEventListener("scroll", () => {
    state.threadNearBottom = isThreadNearBottom();
    if (state.threadNearBottom) showJumpToLatest(false);
  });
}
if (ui.threadJumpLatest) {
  ui.threadJumpLatest.addEventListener("click", () => {
    state.forceScrollToBottom = true;
    scrollThreadToBottom();
    renderTimeline();
  });
}
if (ui.threadComposer) {
  ui.threadComposer.addEventListener("submit", (event) => {
    event.preventDefault();
    const next = state.next;
    if (!next || next.kind !== "answer_clarifications" || next.questions.length !== 1) return;
    submitClarificationAnswers(next);
  });
}
document.querySelectorAll("[data-close-dialog]").forEach((control) => control.addEventListener("click", closeDialog));
window.matchMedia(DESKTOP_QUERY).addEventListener("change", () => applyNavigationLayout());

async function initialize() {
  try {
    const access = await requestJSON("/v1/local-access/status");
	state.localSetupAvailable = Boolean(access.local_setup_available);
    if (!access.authenticated) {
      ui.pairingView.hidden = false;
      ui.workspaceView.hidden = true;
      setConnected(false);
      return;
    }
    await startWorkspace();
    const pending = sessionStorage.getItem(STORAGE_PENDING);
    if (pending) await resumePendingCommand(pending);
  } catch (error) {
    setConnected(false);
    ui.pairingView.hidden = false;
    toast(error.message);
  }
}

setInterval(async () => {
  if (!state.busy && !ui.actionDialog.open && !ui.requestDialog.open && !ui.setupDialog.open && document.visibilityState === "visible") {
    if (state.record) await refreshCurrent(true);
    else {
      try {
        await Promise.all([loadSessions(), loadCompanyActivity(true)]);
        renderSessions();
        renderEmployeesPane();
      } catch {}
    }
  }
}, 5000);

initialize();
