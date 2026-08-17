const BACKGROUND_CONTINUITY_COPY = "この画面を閉じても処理はMacで続きます。次に判断が必要になったら依頼詳細へ表示します。";

const INTERACTION_VERSION = "workspace-interaction.v1";
const COMMAND_VERSION = "workspace-command.v1";
const STORAGE_SESSION = "workcairn.active-session";
const STORAGE_PENDING = "workcairn.pending-command";
const STORAGE_NAV = "workcairn.active-nav";
const STORAGE_ERROR_PREFIX = "workcairn.last-error.";
const LOCAL_PROVIDER_SETUP_TIMEOUT_MS = 180000;
const DESKTOP_QUERY = "(min-width: 900px)";
const LIAISON_ROLE = "Product Manager";

function readPendingCommandsMap() {
  const raw = sessionStorage.getItem(STORAGE_PENDING);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    if (parsed?.command_id && parsed?.version === COMMAND_VERSION) {
      return { [parsed.command_id]: parsed };
    }
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return Object.fromEntries(Object.entries(parsed).filter(([, command]) =>
        command?.command_id && command?.version === COMMAND_VERSION,
      ));
    }
  } catch {}
  return {};
}

function writePendingCommandsMap(map) {
  if (!Object.keys(map).length) sessionStorage.removeItem(STORAGE_PENDING);
  else sessionStorage.setItem(STORAGE_PENDING, JSON.stringify(map));
}

function syncCommandInFlightState() {
  state.commandInFlight = anyPendingCommands().length > 0;
}

function addPendingCommand(command) {
  const map = readPendingCommandsMap();
  map[command.command_id] = command;
  writePendingCommandsMap(map);
  syncCommandInFlightState();
}

function removePendingCommand(commandId) {
  const map = readPendingCommandsMap();
  delete map[commandId];
  writePendingCommandsMap(map);
  syncCommandInFlightState();
}

function anyPendingCommands() {
  return Object.values(readPendingCommandsMap());
}

function pendingCommandForSession(sessionId) {
  if (!sessionId) return null;
  return anyPendingCommands().find((command) => command.payload?.session_id === sessionId) || null;
}

function hasPendingForSession(sessionId) {
  return Boolean(pendingCommandForSession(sessionId));
}

function updateBackgroundWorkingState() {
  const viewingSession = state.record?.session_id;
  const viewingDetail = isRequestDetailVisible();
  const background = anyPendingCommands().some((command) => {
    const sessionId = command.payload?.session_id;
    if (!sessionId) return true;
    return !viewingDetail || sessionId !== viewingSession;
  });
  setBackgroundWorking(background);
}

function shouldBlockPollingRefresh() {
  if (state.busy) return true;
  const viewingSession = state.record?.session_id;
  return Boolean(viewingSession && hasPendingForSession(viewingSession));
}

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
  sessionListFilter: document.querySelector("#session-list-filter"),
  sessionFilterActive: document.querySelector("#session-filter-active"),
  sessionFilterArchived: document.querySelector("#session-filter-archived"),
  timeline: document.querySelector("#activity-timeline"),
  employeeGrid: document.querySelector("#employee-grid"),
  companyFeed: document.querySelector("#company-feed"),
  teamCount: document.querySelector("#team-count"),
  attentionGrid: document.querySelector("#attention-grid"),
  autonomySummary: document.querySelector("#autonomy-summary"),
  proofOfWork: document.querySelector("#proof-of-work"),
  completionHeading: document.querySelector("#completion-heading"),
  requestDialog: document.querySelector("#request-dialog"),
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
  conversation: null,
  conversationError: null,
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
  draftRequest: null,
  threadNearBottom: true,
  forceScrollToBottom: false,
  pendingAttentionTitle: "",
  workflowPlanPreview: null,
  composerDraft: "",
  sessionListFilter: "active",
  sessionMenuSessionId: "",
  sessionConfirmSessionId: "",
  refreshSequence: 0,
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

function requestTitleText(value, max = 48) {
  const text = String(value || "").trim();
  if (!text) return "新しい依頼";
  if (text.length <= max) return text;
  return `${text.slice(0, max - 1)}…`;
}

function truncatePreview(value, max = 120) {
  const normalized = String(value || "").replace(/\s+/g, " ").trim();
  if (!normalized) return "";
  if (normalized.length <= max) return normalized;
  return `${normalized.slice(0, max - 1)}…`;
}

function clearSessionPresentationState() {
  state.conversation = null;
  state.conversationError = null;
  state.evidence = new Map();
  state.workReport = null;
  state.workReportError = null;
  state.detailRenderKey = "";
  state.timelineRenderKey = "";
  state.renderKey = "";
  state.pendingAttentionTitle = "";
  state.workflowPlanPreview = null;
  state.composerDraft = "";
}

function isDraftRequestActive() {
  return Boolean(state.draftRequest);
}

function isArchivedRecord(record = state.record) {
  return record?.archived === true;
}

function closeSessionMenus() {
  state.sessionMenuSessionId = "";
  state.sessionConfirmSessionId = "";
}

function renderSessionListFilter() {
  const archived = state.sessionListFilter === "archived";
  ui.sessionFilterActive.classList.toggle("active", !archived);
  ui.sessionFilterArchived.classList.toggle("active", archived);
  ui.sessionFilterActive.setAttribute("aria-selected", archived ? "false" : "true");
  ui.sessionFilterArchived.setAttribute("aria-selected", archived ? "true" : "false");
}

async function setSessionListFilter(filter, { userInitiated = true } = {}) {
  if (state.sessionListFilter === filter) return;
  closeSessionMenus();
  state.sessionListFilter = filter;
  renderSessionListFilter();
  const viewingArchived = isArchivedRecord();
  if (userInitiated && ((filter === "active" && viewingArchived) || (filter === "archived" && state.record && !viewingArchived))) {
    await clearSelectedSessionPresentation();
  }
  await loadSessions();
  applyNavigationLayout();
}

function sessionListEmptyMessage() {
  return state.sessionListFilter === "archived"
    ? "削除済みの依頼はありません。"
    : "まだ依頼はありません。";
}

async function clearSelectedSessionPresentation() {
  state.refreshSequence += 1;
  state.record = null;
  state.next = null;
  state.lastError = null;
  state.pendingAttentionTitle = "";
  state.workflowPlanPreview = null;
  clearSessionPresentationState();
  localStorage.removeItem(STORAGE_SESSION);
  clearCurrentError();
  clearActionSurface();
  ui.requestSummary.replaceChildren();
  ui.timeline.replaceChildren();
  ui.detailsPanel.hidden = true;
  ui.details.replaceChildren();
  state.renderKey = "";
  state.detailRenderKey = "";
  state.timelineRenderKey = "";
  renderAutonomy();
  renderProofOfWork();
  renderCEOAttention();
  renderComposerState(null);
  if (isRequestDetailVisible()) renderEmpty();
  applyNavigationLayout();
  updateNavDrawerState();
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

function sessionListMeta(record, presentation) {
  const time = record.updated_at || record.created_at;
  const timeLabel = time ? sessionTimeLabel(time) : "";
  return timeLabel ? `${presentation.label} · ${timeLabel}` : presentation.label;
}

function sessionIconNode(icon) {
  const labels = { attention: "対応待ち", working: "作業中", complete: "完了", warning: "失敗" };
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

// inlineMessageActions renders the single "ⓘ エラーの詳細" entry point a
// failure message offers: toggling it reveals a panel with the sanitized
// FailureEnvelope facts and, inside that same panel, the one
// "診断情報をコピー" action -- so there is exactly one place to look for
// error detail and exactly one thing to copy, never two competing controls.
function inlineMessageActions(message) {
  const facts = messageFactsFromDetail(message.detail);
  if (!facts.length && !message.onCopy) return null;
  const panel = node("div", { class: "msg-technical-panel", hidden: true });
  if (facts.length) panel.append(approvalFacts(facts));
  if (message.onCopy) {
    panel.append(node("div", { class: "msg-technical-panel-actions" },
      button("診断情報をコピー", "quiet chip", message.onCopy),
    ));
  }
  const toggle = node("button", {
    class: "icon-button msg-info-toggle msg-info-toggle-labeled",
    type: "button",
    onclick: () => { panel.hidden = !panel.hidden; },
  }, "ⓘ エラーの詳細");
  return node("div", { class: "msg-actions" }, toggle, panel);
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
  state.composerDraft = "";
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
  if (isArchivedRecord()) return "削除済みの依頼です。元に戻すと再び操作できます。";
  if (state.lastError) return "判断が必要です";
  const pending = storedPendingCommand();
  if (pending) {
    if (pending.operation === "interaction.plan.approve_and_execute" || pending.operation === "interaction.workflow.execute") {
      return "Makerの成果物作成、QA担当のReview、必要なRevisionを順番に進めます。";
    }
    if (pending.operation === "interaction.start" || pending.operation === "interaction.plan.generate") {
      return "進め方を準備しています";
    }
    const copy = inFlightCopy(pending.operation);
    return copy.message || copy.title || copy.label;
  }
  if (isDraftRequestActive()) return "依頼内容を入力してください";
  if (!next) return "依頼を選択してください";

  const activeEmployees = activeEmployeeStatuses();
  if (activeEmployees.length === 1) {
    const status = employeeActivityStatusText(activeEmployees[0]);
    if (status) return status;
  } else if (activeEmployees.length > 1) {
    return `${activeEmployees.length}人の社員が作業しています`;
  }

  if (next.kind === "answer_clarifications") {
    // next.questions always names exactly the single next unanswered
    // CEOQuestion (interaction.RecordAnswers commits one durable Turn per
    // answer, in order) -- there is no multi-question progress to track
    // locally.
    return next.questions[0] || "確認の質問";
  }
  if (next.kind === "approve_plan_generation") return "進め方を準備しています";
  if (next.kind === "approve_plan_apply") return "進め方の確認待ちです";
  if (next.kind === "approve_workflow") return "実行承認待ちです";
  if (next.kind === "done" || next.kind === "optional_external_action_or_done") return "完了しました";
  if (next.kind === "inspect_workflow_recovery" || next.kind === "inspect_action_recovery") return "判断が必要です";

  const liaison = (state.companyActivity?.employees || []).find((employee) =>
    employee.is_liaison && employee.display_status === "社長と相談中",
  );
  if (liaison?.name) return `${liaison.name}が社長の回答待ちです`;
  return "作業を進めています";
}

function composerCapabilities(next) {
  if (isDraftRequestActive()) {
    return { enabled: true, placeholder: "依頼内容を入力...", mode: "draft" };
  }
  if (isArchivedRecord()) {
    return { enabled: false, placeholder: "削除済みの依頼です", mode: "archived" };
  }
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
  if (capabilities.mode === "clarification" && state.composerDraft && !ui.composerInput.value) {
    ui.composerInput.value = state.composerDraft;
  }
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
  case "interaction.start":
  case "interaction.plan.generate":
    return { label: "進め方を準備しています", title: "進め方を準備しています", message: "質問または進め方ができるまで、Macで処理を続けます。" };
  case "interaction.plan.approve_and_execute":
  case "interaction.workflow.execute":
    return { label: "仕事を進めています", title: "Makerの成果物作成、QA担当のReview、必要なRevisionを順番に進めます。", message: "Makerの成果物作成、QA担当のReview、必要なRevisionを順番に進めます。" };
  default:
    return { label: "処理中", title: "会社が仕事を進めています", message: "承認済みの処理をMacで安全に続けています。" };
  }
}

function renderInFlight(command) {
  const copy = inFlightCopy(command?.operation);
  clearActionSurface();
  renderComposerState(state.next);
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
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

function formatStructuredFieldShapeEntry(shape) {
  if (!shape || typeof shape !== "object") return "";
  if (!shape.present) return "missing";
  const parts = ["present"];
  if (shape.json_type) parts.push(shape.json_type);
  if (typeof shape.non_blank === "boolean") parts.push(shape.non_blank ? "non_blank" : "blank");
  return parts.join(", ");
}

function structuredFieldShapeExactSummary(shapes, field) {
  const shape = shapes?.[field];
  if (!shape || typeof shape !== "object") return "";
  const parts = [`present: ${shape.present ? "yes" : "no"}`];
  if (shape.json_type) parts.push(`json_type: ${shape.json_type}`);
  if (typeof shape.non_blank === "boolean") parts.push(`non_blank: ${shape.non_blank ? "yes" : "no"}`);
  return parts.join(", ");
}

function structuredStepDescriptionShapeLines(shapes) {
  if (!shapes || typeof shapes !== "object") return [];
  const prefix = "steps.";
  const suffix = ".description";
  return Object.keys(shapes)
    .map((key) => {
      if (!key.startsWith(prefix) || !key.endsWith(suffix)) return null;
      const indexPart = key.slice(prefix.length, key.length - suffix.length);
      if (!/^\d+$/.test(indexPart)) return null;
      const summary = formatStructuredFieldShapeEntry(shapes[key]);
      return summary ? { index: Number(indexPart), line: `- ${key}: ${summary}` } : null;
    })
    .filter(Boolean)
    .sort((left, right) => left.index - right.index)
    .map((entry) => entry.line);
}

function structuredFieldShapeSummary(shapes, field) {
  if (field === "steps.description") {
    const lines = structuredStepDescriptionShapeLines(shapes);
    return lines.length ? lines.join("\n") : "";
  }
  return structuredFieldShapeExactSummary(shapes, field);
}

function structuredFieldShapeFacts(shapes, field) {
  if (field === "steps.description") {
    const lines = structuredStepDescriptionShapeLines(shapes);
    return lines.length ? [["Structured field shapes", lines.join("\n")]] : [];
  }
  const summary = structuredFieldShapeExactSummary(shapes, field);
  return summary ? [[`${field} shape`, summary]] : [];
}

function parseDiagnosticsFacts(error) {
  const parse = error.details?.parse;
  const structuredFields = structuredFieldsSummary(parse?.structured_output_presence);
  const parseFailureReason = error.parse_failure_reason || parse?.reason || "—";
  const parseField = parse?.field || error.parse_failure_field || "—";
  return [
    ["Error code", error.code],
    ["Stage", error.stage || "—"],
    ["Substage", error.substage || "—"],
    ["Category", error.category || "—"],
    ["HTTP status", error.http_status || "—"],
    ["Command ID", error.command_id || "—"],
    ["Request ID", error.request_id || "—"],
    ["Parse reason", parseFailureReason],
    ["Parse field", parseField],
    ...(structuredFields ? [["Structured fields", structuredFields]] : []),
    ...structuredFieldShapeFacts(parse?.structured_output_field_shape, parseField),
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
    node("p", { class: "supporting" }, "自動コピーを利用できませんでした。下の診断情報を長押ししてコピーしてください。"),
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
  if (isDraftRequestActive()) return true;
  if (isDesktopLayout()) return Boolean(state.record) || isDraftRequestActive();
  return state.nav === "request_detail" && (Boolean(state.record) || isDraftRequestActive());
}

function syncRequestDetailNavigation() {
  if (isDesktopLayout()) {
    applyNavigationLayout();
    return;
  }
  const sessionID = localStorage.getItem(STORAGE_SESSION);
  const preferredNav = localStorage.getItem(STORAGE_NAV);
  const shouldKeepDetail = isDraftRequestActive()
    || (sessionID && state.record && preferredNav === "request_detail");
  if (shouldKeepDetail && state.nav !== "request_detail") {
    state.nav = "request_detail";
    applyNavigationLayout();
  }
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
  const showDetail = Boolean(state.record) || isDraftRequestActive();
  const showList = desktop ? !showDetail : state.nav === "request_list";
  ui.menuButton.hidden = desktop;
  ui.requestsPane.classList.toggle("mobile-visible", desktop || state.nav === "request_list" || state.nav === "request_detail" || isDraftRequestActive());
  ui.requestsPane.classList.toggle("has-detail", desktop && showDetail);
  ui.employeesPane.classList.toggle("mobile-hidden", !desktop && state.nav !== "employees_home");
  ui.requestListView.hidden = !showList;
  ui.requestListView.classList.toggle("mobile-hidden", ui.requestListView.hidden);
  ui.requestDetailView.hidden = !showDetail;
  ui.requestDetailView.classList.toggle("mobile-hidden", !desktop && (state.nav !== "request_detail" || !showDetail));
  updateBackgroundWorkingState();
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
  state.draftRequest = null;
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
  if (isDraftRequestActive()) {
    setNav("request_detail");
    renderDraftRequestDetail();
    return;
  }
  if (sessionID && sessionID !== state.record?.session_id) {
    void selectSession(sessionID).then(() => showRequestDetail(sessionID));
    return;
  }
  setNav("request_detail");
  if (state.record) {
    state.renderKey = "";
    renderRequestDetail();
  } else {
    applyNavigationLayout();
  }
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

function interactionErrorGuidance(code, stage = "") {
  const providerFailures = {
    PROVIDER_AUTHENTICATION_REQUIRED: "Claudeの接続を確認してください。credentialが無効・失効している可能性があります。",
    PROVIDER_BILLING_REQUIRED: "Claude側の請求・支払い設定を確認してください。WorkCairnは自動retryしません。",
    PROVIDER_PERMISSION_DENIED: "Claude側で、この接続に必要な利用権限を確認してください。",
    PROVIDER_REQUEST_INVALID: "WorkCairnからClaudeへ送ったrequestが拒否されました。自動retryせず、問い合わせIDを確認してください。",
    PROVIDER_RATE_LIMITED: "Claudeの利用上限に達しました。時間を置き、状態を確認してから新しいCommandとして再開してください。",
    PROVIDER_UNAVAILABLE: "Claudeへ接続できないか、Claude側が一時的に利用できません。自動fallbackやretryは行っていません。",
    PROVIDER_RESPONSE_INVALID: "Claudeから正常に読み取れる応答を受け取れませんでした。自動retryせず、問い合わせIDを確認してください。",
  };
  const reviewContractFailures = {
    REVIEW_PROMPT_FAILED: "レビュー用の指示を組み立てられませんでした。成果物は保持されています。",
    REVIEW_ROUTE_FAILED: "レビュー担当のAIモデルを解決できませんでした。成果物は保持されています。",
    REVIEW_RESULT_INVALID: "AIのレビュー結果を正しく解釈できませんでした。成果物は保持されています。",
  };
  if (code === "PROVIDER_CONFIGURATION_REQUIRED") {
    return "AIサービスの接続設定が不足しています。Providerへ依頼は送信されていません。MacのAI Connectionsから接続してください。";
  }
  if (providerFailures[code]) return providerFailures[code];
  if (reviewContractFailures[code]) return reviewContractFailures[code];
  if (code === "PROJECT_NAME_COLLISION") return "同じ名前の仕事がすでにあります。新しい仕事として作成できませんでした。少し時間を置くか、依頼の表現を変えて改めて送ってください。";
  if (code === "INTERACTION_PLAN_FAILED" && stage === "interaction_plan_commit_cas") {
    return "同じ依頼の状態が先に更新されたため、この進め方は保存していません。新しい状態を確認してください。";
  }
  if (code === "INTERACTION_PLAN_FAILED" &&
    (stage === "ceo_plan_intent" || stage === "ceo_plan_normalization" || stage === "ceo_plan_parser")) {
    return "AIサービスから応答を受信しましたが、安全な進め方として確認できる形式ではありませんでした。進め方は保存・適用されていません。";
  }
  if (code === "INTERACTION_PLAN_FAILED" && stage === "interaction_plan_generation") {
    return "AIサービスで進め方を生成できませんでした。自動retryや別サービスへの切替は行っていません。接続状態を確認してください。";
  }
  if (code === "WORKFLOW_TASK_ASSIGNMENT_REQUIRED") {
    return "担当AIを決められない仕事があるため、実行を開始していません。Organizationを確認してから、改めて実行内容を確認してください。";
  }
  if (code === "WORKFLOW_REVIEWER_ASSIGNMENT_REQUIRED") {
    return "Makerと異なるReviewerを一意に決められなかったため、Workflowを開始していません。OrganizationのQA Engineerを確認してください。";
  }
  return "成立済みの記録を推測で変更せず、現在の状態を確認してください。";
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
  const providerFailures = {
    PROVIDER_AUTHENTICATION_REQUIRED: true,
    PROVIDER_PERMISSION_DENIED: true,
  };
  const providerFailureCopy = code in {
    PROVIDER_AUTHENTICATION_REQUIRED: 1,
    PROVIDER_BILLING_REQUIRED: 1,
    PROVIDER_PERMISSION_DENIED: 1,
    PROVIDER_REQUEST_INVALID: 1,
    PROVIDER_RATE_LIMITED: 1,
    PROVIDER_UNAVAILABLE: 1,
    PROVIDER_RESPONSE_INVALID: 1,
  };
  const providerIssue = providerSetupRequired || providerGenerationFailed || providerFailureCopy;
  const providerSettingsAction = providerSetupRequired || code === "PROVIDER_AUTHENTICATION_REQUIRED" || code === "PROVIDER_PERMISSION_DENIED";
  const pending = storedPendingCommand(state.record?.session_id);
  const errorRenderKey = `error:${JSON.stringify([remembered?.session_id || "", remembered?.session_version || 0, code, stage || "", remembered?.command_id || ""] )}`;
  if (state.renderKey === errorRenderKey) return;
  state.renderKey = errorRenderKey;
  const recoveryAction = providerSettingsAction
    ? button("AI Connectionsを開く", "primary chip", () => openSettingsDialog())
    : providerIssue
      ? button("進め方の作成待ちへ戻る", "primary chip", () => refreshCurrent())
      : pending
        ? button("処理を再確認", "primary chip", () => resumePendingCommand(pending))
        : button("状態を更新", "primary chip", () => refreshCurrent());
  setQuickReplies([
    recoveryAction,
    button("依頼一覧へ", "quiet chip", () => { selectSession(null); showRequestList(); }),
  ]);
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
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
  renderSessionListFilter();
  await Promise.all([loadProviderStatus(), loadWorkspaceStatus(), loadOrganization().catch(() => null), loadCompanyActivity().catch(() => null)]);
  const stored = localStorage.getItem(STORAGE_SESSION);
  if (stored) {
    try {
      const record = await requestJSON(`/v1/interactions/${encodeURIComponent(stored)}`);
      state.sessionListFilter = record.archived ? "archived" : "active";
      renderSessionListFilter();
      await loadSessions();
      const preferredNav = localStorage.getItem(STORAGE_NAV);
      await selectSession(stored, { rememberNav: false });
      if (preferredNav === "request_detail" || isDesktopLayout()) showRequestDetail(stored);
      else if (preferredNav === "request_list" && !isDesktopLayout()) setNav("request_list", false);
      else setNav("employees_home", false);
      if (state.workspaceStatus && (!state.workspaceStatus.organization_ready || !state.providerStatus?.configured)) openSetupWizard();
      return;
    } catch {
      localStorage.removeItem(STORAGE_SESSION);
    }
  }
  await loadSessions();
  const candidate = state.sessions.find((record) => !["completed", "action_completed"].includes(record.state)) || state.sessions[0];
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
  const query = state.sessionListFilter === "archived" ? "?archived=true" : "";
  state.sessions = await requestJSON(`/v1/interactions${query}`);
  state.sessions.sort((left, right) => String(right.created_at).localeCompare(String(left.created_at)));
  renderSessionListFilter();
  renderSessions();
}

async function resolveSessionExpectedVersion(sessionID, fallbackVersion = null) {
  if (state.record?.session_id === sessionID && state.next?.expected_version != null) {
    return state.next.expected_version;
  }
  if (state.record?.session_id === sessionID && state.record?.version != null) {
    return state.record.version;
  }
  if (fallbackVersion != null) return fallbackVersion;
  const detail = await requestJSON(`/v1/interactions/${encodeURIComponent(sessionID)}`);
  return detail.version;
}

async function executeArchiveToggleCommand(operation, payload, busyTitle, busyMessage, onSuccess) {
  const sessionId = payload.session_id;
  if (hasPendingForSession(sessionId)) {
    toast("同じ処理を実行中です。完了するまでお待ちください。");
    return false;
  }
  let accepted = false;
  let command = null;
  try {
    command = {
      version: COMMAND_VERSION,
      command_id: commandID(),
      operation,
      approved: true,
      payload,
    };
    addPendingCommand(command);
    const affectsView = sessionId === state.record?.session_id && isRequestDetailVisible();
    if (affectsView) {
      state.activeCommandID = command.command_id;
      state.renderKey = "";
      renderInFlight(command);
    }
    setBusy(true, busyTitle, busyMessage);
    await requestJSON("/v1/commands", {
      method: "POST",
      headers: { Prefer: "respond-async" },
      body: JSON.stringify(command),
    });
    accepted = true;
    await monitorAcceptedCommand(command);
    await onSuccess();
    setBusy(false);
    return true;
  } catch (error) {
    if (!accepted && command?.command_id) removePendingCommand(command.command_id);
    showError(error);
    return false;
  } finally {
    if (command?.command_id && state.activeCommandID === command.command_id) state.activeCommandID = "";
    updateBackgroundWorkingState();
  }
}

async function confirmArchiveSession(record) {
  closeSessionMenus();
  renderSessions();
  const sessionID = record.session_id;
  const wasSelected = state.record?.session_id === sessionID;
  let expectedVersion;
  try {
    expectedVersion = await resolveSessionExpectedVersion(sessionID, record.version);
  } catch (error) {
    showError(error, "依頼の状態を取得できませんでした");
    return;
  }
  const success = await executeArchiveToggleCommand(
    "interaction.archive",
    { session_id: sessionID, expected_version: expectedVersion, current_time: now() },
    "一覧から非表示にしています",
    "成果物や実行記録は保持したまま、依頼一覧から外します。",
    async () => {
      if (wasSelected) await clearSelectedSessionPresentation();
      await setSessionListFilter("active", { userInitiated: false });
      await loadCompanyActivity(true).catch(() => null);
      renderEmployeesPane();
      applyNavigationLayout();
      updateNavDrawerState();
    },
  );
  if (success) toast("依頼一覧から非表示にしました。");
}

async function confirmUnarchiveSession() {
  const record = state.record;
  if (!record || !isArchivedRecord(record)) return;
  const sessionID = record.session_id;
  let expectedVersion;
  try {
    expectedVersion = await resolveSessionExpectedVersion(sessionID, record.version);
  } catch (error) {
    showError(error, "依頼の状態を取得できませんでした");
    return;
  }
  const success = await executeArchiveToggleCommand(
    "interaction.unarchive",
    { session_id: sessionID, expected_version: expectedVersion, current_time: now() },
    "依頼を復元しています",
    "削除済み一覧から依頼一覧へ戻します。",
    async () => {
      await setSessionListFilter("active", { userInitiated: false });
      await refreshCurrent(true);
      showRequestDetail(sessionID);
    },
  );
  if (success) toast("依頼を一覧に戻しました。");
}

async function syncSessionListFilterToRecord() {
  if (!state.record?.session_id || typeof state.record.archived !== "boolean") return;
  const shouldBeArchived = state.record.archived;
  const filterIsArchived = state.sessionListFilter === "archived";
  if (shouldBeArchived === filterIsArchived) return;
  await setSessionListFilter(shouldBeArchived ? "archived" : "active", { userInitiated: false });
}

async function selectSession(id, options = {}) {
  closeSessionMenus();
  state.refreshSequence += 1;
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
    if (!isDraftRequestActive()) {
      renderEmpty();
      renderRequestDetail();
    }
    renderEmployeesPane();
    applyNavigationLayout();
    return;
  }
  state.draftRequest = null;
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
  if (isDraftRequestActive()) return;
  const id = localStorage.getItem(STORAGE_SESSION);
  if (!id) {
    renderEmpty();
    return;
  }
  const sequence = ++state.refreshSequence;
  try {
    const [record, next, reportResult, conversationResult] = await Promise.all([
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}`),
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}/next`),
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}/work-report`)
        .then((report) => ({ report }))
        .catch((error) => ({ error })),
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}/conversation`)
        .then((conversation) => ({ conversation }))
        .catch((error) => ({ error })),
    ]);
    if (sequence !== state.refreshSequence) return;
    state.record = record;
    state.next = next;
    state.workReport = reportResult.report || null;
    state.workReportError = reportResult.error || null;
    state.conversation = conversationResult.conversation || null;
    state.conversationError = conversationResult.error || null;
    restoreError(record);
    if (!state.lastError) await restoreDurableFailure(next);
    await loadTaskEvidenceDetails().catch(() => null);
    if (sequence !== state.refreshSequence) return;
    setConnected(true);
    await syncSessionListFilterToRecord();
    if (sequence !== state.refreshSequence) return;
    renderRequestDetail();
    syncRequestDetailNavigation();
    await loadSessions();
    if (sequence !== state.refreshSequence) return;
    await loadCompanyActivity().catch(() => null);
    renderEmployeesPane();
  } catch (error) {
    if (sequence !== state.refreshSequence) return;
    setConnected(false);
    showError(error, silent ? "Macとの接続を確認してください" : "依頼の状態を取得できませんでした");
  }
}

function renderEmpty() {
  if (!isRequestDetailVisible()) return;
  clearActionSurface();
  renderComposerState(null);
  if (isDraftRequestActive()) {
    renderDraftRequestDetail();
    return;
  }
  ui.activeCard.hidden = false;
  ui.activeCard.replaceChildren(
    node("p", { class: "composer-note" }, "依頼した後はAI社員が計画・実行・レビューを進め、必要な質問と承認だけをここに表示します。"),
  );
  setQuickReplies([button("＋ 新規作成", "primary chip", openNewRequestDraft)]);
  renderTimeline();
}

function renderSessions() {
  if (!state.sessions.length) {
    ui.sessionList.replaceChildren(node("p", { class: "empty" }, sessionListEmptyMessage()));
    return;
  }
  const activeID = state.record?.session_id;
  const groups = groupSessionsByDate(state.sessions);
  ui.sessionList.replaceChildren(...groups.flatMap((group) => [
    node("section", { class: "session-date-group" },
      node("h2", { class: "session-date-label" }, group.label),
      ...group.items.map((record) => renderSessionRow(record, activeID)),
    ),
  ]));
}

function renderSessionRow(record, activeID) {
  const presentation = sessionListPresentation(record);
  const isActive = activeID === record.session_id;
  const menuOpen = state.sessionMenuSessionId === record.session_id;
  const confirmOpen = state.sessionConfirmSessionId === record.session_id;
  const title = requestTitleText(record.request);
  const rowChildren = [
    node("button", {
      class: `session-item${isActive ? " active" : ""}`,
      type: "button",
      onclick: async () => {
        closeSessionMenus();
        await selectSession(record.session_id);
        showRequestDetail(record.session_id);
      },
    },
    sessionIconNode(presentation.icon),
    node("span", { class: "session-copy" },
      node("span", { class: "session-title" }, title),
      node("span", { class: "session-meta" }, sessionListMeta(record, presentation)),
    ),
    ),
  ];
  if (state.sessionListFilter === "active") {
    rowChildren.push(node("button", {
      class: "session-menu-button",
      type: "button",
      "aria-label": `${title}の操作`,
      "aria-expanded": menuOpen || confirmOpen ? "true" : "false",
      "aria-haspopup": "menu",
      onclick: (event) => {
        event.stopPropagation();
        if (confirmOpen) return;
        state.sessionMenuSessionId = menuOpen ? "" : record.session_id;
        state.sessionConfirmSessionId = "";
        renderSessions();
      },
    }, "…"));
  }
  const row = node("div", {
    class: `session-row${isActive ? " active" : ""}${menuOpen || confirmOpen ? " menu-open" : ""}`,
  }, ...rowChildren);
  if (confirmOpen) {
    row.append(node("div", {
      class: "session-archive-confirm",
      role: "dialog",
      "aria-labelledby": `archive-confirm-${record.session_id}`,
    },
    node("p", { id: `archive-confirm-${record.session_id}`, class: "session-archive-confirm-copy" },
      "依頼一覧から非表示にします。成果物や会社の実行記録は保持されます。"),
    node("div", { class: "session-archive-confirm-actions" },
      button("キャンセル", "quiet chip", () => {
        closeSessionMenus();
        renderSessions();
      }),
      button("履歴から削除", "primary chip", () => confirmArchiveSession(record)),
    ),
    ));
  } else if (menuOpen && state.sessionListFilter === "active") {
    row.append(node("div", { class: "session-menu-panel", role: "menu" },
      node("button", {
        class: "session-menu-item",
        type: "button",
        role: "menuitem",
        onclick: (event) => {
          event.stopPropagation();
          state.sessionMenuSessionId = "";
          state.sessionConfirmSessionId = record.session_id;
          renderSessions();
        },
      }, "履歴から削除"),
    ));
  }
  return row;
}

function renderNext(force = false) {
  const next = state.next;
  if (!next) return renderEmpty();
  if (isArchivedRecord()) return renderArchivedSessionView(force);
  const pendingCommand = storedPendingCommand(next.session_id);
  const pendingForSession = Boolean(pendingCommand);
  const pendingInForeground = pendingForSession && isRequestDetailVisible();
  updateBackgroundWorkingState();
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

function renderArchivedSessionView(force = false) {
  const key = `archived:${state.record?.session_id}:${state.record?.version}:${state.conversation?.entries?.length || 0}`;
  if (!force && state.renderKey === key) return;
  state.renderKey = key;
  state.pendingAttentionTitle = "";
  clearActionSurface();
  renderComposerState(state.next);
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
  setQuickReplies([button("元に戻す", "primary chip", confirmUnarchiveSession)]);
  state.forceScrollToBottom = true;
  renderTimeline();
}

function renderRememberedError(error) {
  setQuickReplies([
    button(error.command_id ? "処理を再確認" : "状態を更新", "primary chip", () => error.command_id
      ? inspectCommands([{ scope: "workspace", command_id: error.command_id }])
      : refreshCurrent()),
    button("再読み込み", "quiet chip", async () => { clearCurrentError(); state.renderKey = ""; await refreshCurrent(); }),
  ]);
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
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
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
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
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
  setQuickReplies([]);
  state.forceScrollToBottom = true;
  renderTimeline();
}

// submitClarificationAnswers durably commits exactly the single question
// currently displayed (next.questions[0]) the moment the CEO sends it --
// it never accumulates answers in local state waiting for a later batch
// submit. interaction.RecordAnswers records this one answer as its own
// append-only Turn immediately, so it survives reload and daemon restart
// even if the CEO never answers the next question. Go alone decides
// whether that leaves more questions pending (next.kind stays
// "answer_clarifications") or clarification is complete (Plan generation
// runs, chained server-side).
function submitClarificationAnswers(next) {
  const answer = ui.composerInput.value.trim();
  if (!answer) return toast("回答を入力してください。");
  const currentQuestion = next.questions[0];
  ui.composerInput.value = "";
  state.composerDraft = "";
  return executeNextCommand(next, {
    session_id: next.session_id,
    expected_version: next.expected_version,
    answers: [{ question: currentQuestion, answer }],
    current_time: now(),
  }, "回答を保存しています", "回答後に進め方を準備します。");
}

function currentPlan() {
  if (!state.record?.turns?.length) return null;
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

function planTaskDisplayTitle(task) {
  const title = String(task?.title ?? "").trim();
  if (title) return title;
  const rationale = String(task?.rationale ?? "").trim();
  if (rationale) return rationale;
  return "";
}

function planTaskAssigneeIdentity(task) {
  if (task?.assignee_id) return employeeIdentityByID(task.assignee_id);
  return employeeIdentityByRole(task?.required_role);
}

function planStepCopy(task, index) {
  return `${index + 1}. ${roleLabel(task.required_role)}が${planTaskDisplayTitle(task)}`;
}

function renderPlanApproval(next) {
  const current = currentPlan();
  if (!current) return showError(new Error("Plan evidence is missing"));
  const identifier = localStorage.getItem(`workcairn.project.${state.record.session_id}`) || projectID();
  localStorage.setItem(`workcairn.project.${state.record.session_id}`, identifier);
  setQuickReplies([
    button("この内容で進める", "primary chip", () => {
      executeNextCommand(next, {
        session_id: next.session_id, expected_version: next.expected_version,
        project_id: identifier, plan_digest: current.digest, current_time: now(),
      }, "仕事を開始しています", "Planの適用とReviewed WorkflowをMacで進めています。");
    }),
  ]);
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
  state.forceScrollToBottom = true;
  renderTimeline();
}

async function loadOrganization(force = false) {
  if (force || !state.organization) state.organization = await requestJSON("/v1/organization");
  return state.organization;
}

async function renderWorkflowApproval(next) {
  state.workflowPlanPreview = null;
  setQuickReplies([
    button("実行内容を確認", "primary chip", () => prepareWorkflowApproval(next, 20)),
    button("今は実行しない", "quiet chip", () => toast("仕事は開始されていません。")),
  ]);
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
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
    ui.activeCard.hidden = true;
    ui.activeCard.replaceChildren();
    state.forceScrollToBottom = true;
    renderTimeline();
  } catch (error) {
    showError(error, "実行内容を確認できませんでした");
  }
}

function showCompletionHeading() {
  if (ui.completionHeading) ui.completionHeading.hidden = false;
}

function renderCompletion(next) {
  setQuickReplies([]);
  showCompletionHeading();
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
  renderComposerState(next);
  renderTimeline();
}

function renderDone() {
  setQuickReplies([]);
  showCompletionHeading();
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
  renderComposerState(state.next);
  renderTimeline();
}

function renderAttention(next, title) {
  setQuickReplies([
    button("詳細を確認", "primary chip", () => inspectCommands(next.commands || [])),
    button("状態を更新", "quiet chip", () => refreshCurrent()),
  ]);
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
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
    ui.activeCard.hidden = true;
    ui.activeCard.replaceChildren();
    state.pendingAttentionTitle = "処理記録を確認してください。";
    renderTimeline();
  } catch (error) {
    showError(error, "処理記録を取得できませんでした");
  }
}

function closeActionDialog() { if (ui.actionDialog.open) ui.actionDialog.close(); }

function approvalFacts(facts) {
  return node("div", { class: "approval-box" }, node("dl", {}, ...facts.map(([term, value]) =>
    node("div", {}, node("dt", {}, term), node("dd", { class: String(value).startsWith("sha256:") ? "digest" : "" }, value || "—")),
  )));
}

async function executeNextCommand(next, payload, busyTitle, busyMessage, fixedCommandID = null) {
  const sessionId = payload.session_id;
  if (hasPendingForSession(sessionId)) {
    toast("同じ処理を実行中です。完了するまでお待ちください。");
    return false;
  }
  let accepted = false;
  let command = null;
  try {
    command = {
      version: COMMAND_VERSION,
      command_id: fixedCommandID || commandID(),
      operation: next.operation,
      approved: true,
      payload,
    };
    addPendingCommand(command);
    state.activeCommandID = command.command_id;
    state.renderKey = "";
    renderInFlight(command);
    setBusy(false);
    updateBackgroundWorkingState();
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
    if (!accepted && command?.command_id) removePendingCommand(command.command_id);
    showError(error);
    return false;
  } finally {
    if (command?.command_id && state.activeCommandID === command.command_id && !pendingCommandForSession(sessionId)) {
      state.activeCommandID = "";
    }
    updateBackgroundWorkingState();
  }
}

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function monitorAcceptedCommand(command) {
  setBusy(false);
  const sessionId = command.payload?.session_id;
  const viewingSession = localStorage.getItem(STORAGE_SESSION) === sessionId && isRequestDetailVisible();
  updateBackgroundWorkingState();
  if (viewingSession) {
    state.activeCommandID = command.command_id;
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
      removePendingCommand(command.command_id);
      if (state.activeCommandID === command.command_id) state.activeCommandID = "";
      updateBackgroundWorkingState();
      state.renderKey = "";
      return record;
    }
    if (record.state === "failed" || record.state === "partial_failure") {
      removePendingCommand(command.command_id);
      if (state.activeCommandID === command.command_id) state.activeCommandID = "";
      updateBackgroundWorkingState();
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
	  updateBackgroundWorkingState();
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

function storedPendingCommand(sessionId = null) {
  const scopedSession = sessionId || state.record?.session_id || localStorage.getItem(STORAGE_SESSION);
  if (scopedSession) return pendingCommandForSession(scopedSession);
  return null;
}

async function resumePendingCommand(command) {
  if (typeof command === "string") {
    try {
      command = JSON.parse(command);
    } catch {
      sessionStorage.removeItem(STORAGE_PENDING);
      return;
    }
  }
  if (command?.version !== COMMAND_VERSION || typeof command.command_id !== "string" || !command.command_id) {
    removePendingCommand(command?.command_id);
    return;
  }
  try {
    await monitorAcceptedCommand(command);
    if (command.payload?.session_id === state.record?.session_id) await refreshCurrent();
    setBusy(false);
    toast("Macで完了した処理を反映しました。");
  } catch (error) {
    updateBackgroundWorkingState();
    if (command.payload?.session_id === state.record?.session_id) {
      showError(error, "前回のCommand状態を確認できませんでした");
    }
  }
}

async function resumeAllPendingCommands() {
  const commands = anyPendingCommands();
  if (!commands.length) return;
  await Promise.all(commands.map((command) => resumePendingCommand(command)));
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

function actorRoleLabel(actorRef) {
  if (!actorRef?.employee_id) return "";
  const employee = (state.organization?.inventory?.employees || []).find((candidate) => candidate.id === actorRef.employee_id);
  return employee?.role ? roleLabel(employee.role) : "";
}

function failureEnvelopeFacts(envelope) {
  if (!envelope) return [];
  return parseDiagnosticsFacts({
    code: envelope.code,
    stage: envelope.stage,
    substage: envelope.substage,
    category: envelope.category,
    http_status: envelope.provider?.http_status,
    command_id: envelope.child_command_id,
    request_id: envelope.provider?.request_id,
    parse_failure_reason: envelope.parse?.reason,
    parse_failure_field: envelope.parse?.field,
    details: envelope,
  });
}

// reviewIssueLines formats canonical review_issues[] (description/suggested_action
// only) -- the sole source for Request Changes content. Never substitutes
// an internal status, verdict, or error value when issues are absent.
function reviewIssueLines(issues) {
  const lines = [];
  for (const issue of issues || []) {
    if (!issue.description) continue;
    lines.push("");
    lines.push(`・${issue.description}`);
    if (issue.suggested_action) lines.push(`  対応案: ${issue.suggested_action}`);
  }
  return lines;
}

function companyFactText(entry) {
  const subjectName = entry.subject?.name || entry.subject?.employee_id || "";
  switch (entry.kind) {
  case "task_assigned":
    return entry.task_title ? `${subjectName}に${entry.task_title}を割り当てました。` : `${subjectName}に仕事を割り当てました。`;
  case "deliverable_ready":
    return `${subjectName}が成果物を作成しました。`;
  case "review_approved":
    // canonical review_summary only -- never an internal status/verdict/error
    // value, and never guessed when the canonical summary is absent.
    return entry.review_summary
      ? `${subjectName}のレビューが完了しました。\n\n${entry.review_summary}`
      : `${subjectName}のレビューが完了しました。`;
  case "review_request_changes": {
    // Reached only when Reviewer/Maker are not both canonically confirmed
    // (Directed Communication is not possible) -- still shows the real
    // canonical review_issues content, never a bare fallback.
    const lines = [`${subjectName}から修正を依頼されました。`, ...reviewIssueLines(entry.review_issues)];
    return lines.join("\n");
  }
  case "revision_completed":
    return `${subjectName}が修正しました。`;
  case "task_completed":
    return entry.task_title ? `${entry.task_title}が完了しました。` : "仕事が完了しました。";
  case "request_completed":
    return "依頼が完了しました。";
  default:
    return entry.task_title || "";
  }
}

function directedCommunicationText(entry) {
  if (entry.kind === "review_request_changes") {
    return ["修正をお願いします。", ...reviewIssueLines(entry.review_issues)].join("\n");
  }
  if (entry.kind === "revision_completed") return "修正が完了しました。";
  return "";
}

function conversationEntryNode(entry) {
  if (entry.category === "ceo_message") {
    return node("article", { class: "msg msg-user" },
      node("div", { class: "msg-header" },
        node("span", { class: "msg-name" }, "あなた"),
        entry.at ? node("time", { class: "msg-time-inline" }, sessionTimeLabel(entry.at)) : null,
      ),
      node("div", { class: "msg-body msg-body-user" },
        node("p", { class: "msg-text" }, entry.ceo_message_text || ""),
      ),
    );
  }
  if (entry.category === "directed_communication") {
    const speakerName = entry.speaker?.name || entry.speaker?.employee_id || "";
    const role = actorRoleLabel(entry.speaker);
    const mention = entry.mention_allowed && entry.recipient?.name
      ? node("p", { class: "msg-mention" }, `@${entry.recipient.name}`)
      : null;
    return node("article", { class: "msg msg-employee msg-directed" },
      node("div", { class: "msg-header" },
        node("span", { class: "msg-name" }, speakerName),
        node("div", { class: "msg-meta" },
          role ? node("span", { class: "msg-role" }, role) : null,
          entry.at ? node("time", { class: "msg-time-inline" }, sessionTimeLabel(entry.at)) : null,
        ),
      ),
      node("div", { class: "msg-body msg-body-employee" },
        mention,
        node("p", { class: "msg-text" }, directedCommunicationText(entry)),
      ),
    );
  }
  if (entry.category === "company_fact") {
    const text = companyFactText(entry);
    const viewer = entry.kind === "deliverable_ready" ? deliverableViewerNode(entry) : null;
    return node("article", { class: "msg msg-company-fact" },
      node("div", { class: "msg-company-fact-body" },
        text ? node("p", { class: "msg-company-fact-copy" }, text) : null,
        viewer,
        entry.at ? node("time", { class: "msg-company-fact-time" }, sessionTimeLabel(entry.at)) : null,
      ),
    );
  }
  if (entry.kind === "clarification_requested") {
    return clarificationQuestionNode(entry);
  }
  if (entry.category === "system" || entry.kind === "failure") {
    const facts = failureEnvelopeFacts(entry.failure_details);
    const guidance = interactionErrorGuidance(entry.failure_details?.code, entry.failure_details?.stage);
    const actions = facts.length
      ? inlineMessageActions({ detail: facts, onCopy: () => copySanitizedError({
        code: entry.failure_details?.code,
        stage: entry.failure_details?.stage,
        substage: entry.failure_details?.substage,
        category: entry.failure_details?.category,
        http_status: entry.failure_details?.provider?.http_status,
        command_id: entry.failure_details?.child_command_id,
        request_id: entry.failure_details?.provider?.request_id,
        parse_failure_reason: entry.failure_details?.parse?.reason,
        parse_failure_field: entry.failure_details?.parse?.field,
        details: entry.failure_details,
      }) })
      : null;
    return node("article", { class: "msg msg-system msg-failure attention" },
      node("div", { class: "msg-system-rule", "aria-hidden": "true" }),
      node("p", { class: "msg-system-copy" }, `処理を完了できませんでした。\n\n${guidance} 自動retryせず、次の判断を待っています。`),
      actions,
      node("div", { class: "msg-system-rule", "aria-hidden": "true" }),
    );
  }
  // Every known Category/Kind is handled above; this only guards against a
  // future, not-yet-handled entry shape. It must never fall back to an
  // internal enum value (entry.kind) as if it were message content.
  return node("article", { class: "msg msg-system" },
    node("p", { class: "msg-system-copy" }, entry.task_title || ""),
  );
}

// clarificationQuestionNode renders WorkCairn's own already-committed
// clarification question (ConversationEntry.question, ADR-0047/CP3+) --
// never a UI-composed or locally-drafted string, so the question stays
// visible in the timeline exactly as WorkCairn asked it, before and after
// the CEO answers, and across reload/daemon restart.
function clarificationQuestionNode(entry) {
  return node("article", { class: "msg msg-system msg-clarification" },
    node("div", { class: "msg-header" },
      node("span", { class: "msg-name" }, "WorkCairn"),
      entry.at ? node("time", { class: "msg-time-inline" }, sessionTimeLabel(entry.at)) : null,
    ),
    node("div", { class: "msg-body msg-body-clarification" },
      node("p", { class: "msg-text" }, entry.question || ""),
    ),
  );
}

// appliedProjectName reads the Project name the Session's own canonical
// plan_applied Turn already committed -- never guessed, never derived from
// a ConversationEntry (which deliberately does not repeat it per Task/entry).
function appliedProjectName() {
  const applied = [...(state.record?.turns || [])].reverse().find((turn) => turn.kind === "plan_applied");
  return applied?.project_name || "";
}

// deliverableViewerNode reuses the existing read-only
// /v1/projects/{project}/tasks/{task}/evidence projection (the same one
// the request-detail side panel already fetches into state.evidence) to
// let the CEO open a completed Deliverable directly from the "成果物を作成
// しました" message -- never composing or caching a copy of the Deliverable
// body itself; the canonical Deliverable stays the source of truth.
function deliverableViewerNode(entry) {
  const projectName = appliedProjectName();
  if (!projectName || !entry.task_id) return null;
  const panel = node("div", { class: "msg-deliverable-panel", hidden: true });
  const toggle = node("button", {
    class: "icon-button msg-deliverable-toggle",
    type: "button",
    onclick: async () => {
      const opening = panel.hidden;
      panel.hidden = !panel.hidden;
      if (opening) await fillDeliverablePanel(panel, projectName, entry.task_id);
    },
  }, "成果物を見る");
  return node("div", { class: "msg-actions" }, toggle, panel);
}

// Always re-fetches from the canonical evidence endpoint on every open --
// a Task's Deliverable can change across a Request Changes -> Revision
// cycle, so a value cached from an earlier point in the same session must
// never be shown as if it were still current. state.evidence is still
// updated so the existing request-detail side panel can reuse the fresh
// copy, but this viewer never trusts what it finds there.
async function fillDeliverablePanel(panel, projectName, taskId) {
  const key = `${projectName}/${taskId}`;
  panel.replaceChildren(node("p", { class: "supporting" }, "成果物を確認しています…"));
  let evidence;
  try {
    evidence = await requestJSON(`/v1/projects/${encodeURIComponent(projectName)}/tasks/${encodeURIComponent(taskId)}/evidence`);
    state.evidence.set(key, evidence);
  } catch (error) {
    panel.replaceChildren(node("p", { class: "warning" }, `成果物を取得できませんでした: ${error.message}`));
    return;
  }
  const deliverable = evidence.deliverable;
  panel.replaceChildren(
    deliverable
      ? node("pre", { class: "deliverable-preview" }, deliverable.content || "（本文なし）")
      : node("p", { class: "supporting" }, "成果物はまだcommitされていません。"),
  );
}

function planPendingNode(current) {
  return node("article", { class: "msg msg-plan-pending" },
    node("div", { class: "msg-body" }, planEmbedNode(current.plan)),
  );
}

function pendingInteractionNodes() {
  const next = state.next;
  if (!next) return [];
  const pending = storedPendingCommand();
  if (pending) return [];
  if (state.lastError) {
    const hasConversationFailure = (state.conversation?.entries || []).some(
      (entry) => entry.category === "system" && entry.kind === "failure",
    );
    if (hasConversationFailure) return [];
    const guidance = interactionErrorGuidance(state.lastError.code, state.lastError.stage);
    return [node("article", { class: "msg msg-system msg-failure attention" },
      node("div", { class: "msg-system-rule", "aria-hidden": "true" }),
      node("p", { class: "msg-system-copy" }, `${state.lastError.title || "処理を完了できませんでした。"}\n\n${guidance} 自動retryせず、次の判断を待っています。`),
      inlineMessageActions({
        detail: parseDiagnosticsFacts(state.lastError),
        onCopy: () => copySanitizedError(state.lastError),
      }),
      node("div", { class: "msg-system-rule", "aria-hidden": "true" }),
    )];
  }
  if (state.pendingAttentionTitle) {
    return [node("article", { class: "msg msg-system attention" },
      node("p", { class: "msg-system-copy" }, state.pendingAttentionTitle),
    )];
  }
  switch (next.kind) {
  case "approve_plan_generation":
    if (!state.providerStatus?.configured) {
      return [node("article", { class: "msg msg-system attention" },
        node("p", { class: "msg-system-copy" }, "AIサービスへ接続してください。"),
      )];
    }
    // Composer status already carries the single-line state copy.
    return [];
  case "answer_clarifications":
    // The current unanswered question is already visible via the
    // canonical ConversationEntry (clarification_requested) rendered in
    // conversationTimelineNodes() -- composer status carries progress,
    // placeholder stays a generic input hint.
    return [];
  case "approve_plan_apply": {
    const current = currentPlan();
    return current ? [planPendingNode(current)] : [];
  }
  case "approve_workflow":
    return [node("article", { class: "msg msg-system" },
      node("p", { class: "msg-system-copy" }, "Makerとは別のQA Reviewerを、役割と許可範囲から自動選択します。今回任せる仕事ステップの上限は20件です。"),
    )];
  default:
    return [];
  }
}

// conversationTimelineNodes renders canonical conversation entries
// directly, with no client-side filtering or reconstruction: the
// Conversation Projection itself (process.InspectConversation) now
// reveals clarification questions one at a time, interleaved with their
// answers, exactly as WorkCairn durably committed them (see
// clarificationAnswerEntries in conversation.go) -- there is no "every
// question up front" shape left for the UI to filter down from, and no
// question/answer array the UI zips together to reconstruct history.
function conversationTimelineNodes() {
  const nodes = (state.conversation?.entries || []).map((entry) => conversationEntryNode(entry));
  nodes.push(...pendingInteractionNodes());
  return nodes;
}

function timelineRenderKey() {
  const entries = state.conversation?.entries || [];
  return JSON.stringify([
    entries.map((entry) => [
      entry.at, entry.category, entry.kind, entry.mention_allowed, entry.ceo_message_text,
      entry.review_summary, entry.review_issues, entry.subject?.employee_id, entry.speaker?.employee_id,
      entry.question, entry.task_id,
    ]),
    state.next?.kind,
    state.next?.expected_version,
    storedPendingCommand()?.command_id || "",
    state.lastError?.code || "",
    currentPlan()?.digest || "",
    state.pendingAttentionTitle || "",
  ]);
}

function planEmbedNode(plan) {
  if (!plan?.proposed_tasks?.length) return null;
  return node("div", { class: "msg-embed msg-embed-plan" },
    node("div", { class: "msg-attach" },
      node("p", { class: "msg-attach-label" }, "進め方"),
      node("ul", { class: "msg-attach-list" }, ...plan.proposed_tasks.map((task) => {
        const identity = planTaskAssigneeIdentity(task);
        const title = planTaskDisplayTitle(task);
        return node("li", {},
          node("strong", {}, identity.name),
          title ? node("span", { class: "msg-attach-task-title" }, title) : null,
        );
      })),
    ),
  );
}

function renderTimeline() {
  if (isDraftRequestActive()) {
    const key = "draft-empty";
    if (state.timelineRenderKey === key) return;
    state.timelineRenderKey = key;
    ui.timeline.replaceChildren(node("p", { class: "empty" }, "依頼内容を入力してください。"));
    return;
  }
  const nodes = conversationTimelineNodes();
  const key = timelineRenderKey();
  const shouldFollow = state.forceScrollToBottom || state.threadNearBottom;
  if (state.timelineRenderKey === key) {
    if (state.forceScrollToBottom) {
      requestAnimationFrame(() => scrollThreadToBottom());
      state.forceScrollToBottom = false;
    }
    return;
  }
  state.timelineRenderKey = key;
  ui.timeline.replaceChildren(...(nodes.length ? nodes : [node("p", { class: "empty" }, "依頼すると、会社の動きがここに残ります。")]));
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
  const sessionID = state.record?.session_id;
  if (!sessionID || isDraftRequestActive()) return;
  const workflow = [...(state.record?.turns || [])].reverse().find((turn) => turn.workflow)?.workflow;
  if (!workflow) return;
  const missing = workflow.tasks.filter((task) => !state.evidence.has(`${workflow.project_name}/${task.task_id}`));
  if (!missing.length) return;
  try {
    const results = await Promise.all(missing.map((task) => requestJSON(`/v1/projects/${encodeURIComponent(workflow.project_name)}/tasks/${encodeURIComponent(task.task_id)}/evidence`)));
    if (state.record?.session_id !== sessionID || isDraftRequestActive()) return;
    results.forEach((evidence, index) => state.evidence.set(`${workflow.project_name}/${missing[index].task_id}`, evidence));
    renderDetails();
    renderRequestDetail();
  } catch (error) {
    if (state.record?.session_id === sessionID && !isDraftRequestActive()) {
      toast(`成果物の詳細を取得できませんでした: ${error.message}`);
    }
  }
}

function renderRequestDetail() {
  if (isDraftRequestActive()) {
    renderDraftRequestDetail();
    return;
  }
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
  syncRequestDetailNavigation();
  renderNext();
  renderRequestSummary();
  renderDetails();
  renderTimeline();
  renderAutonomy();
  renderProofOfWork();
  renderCEOAttention();
  renderComposerState(state.next);
}

function renderDraftRequestDetail() {
  ui.requestSummary.replaceChildren(
    node("h1", { class: "thread-title" }, "新しい依頼"),
  );
  clearActionSurface();
  ui.activeCard.hidden = true;
  ui.activeCard.replaceChildren();
  ui.timeline.replaceChildren(node("p", { class: "empty" }, "依頼内容を入力してください。"));
  state.timelineRenderKey = "draft-empty";
  ui.detailsPanel.hidden = true;
  ui.details.replaceChildren();
  ui.composerInput.value = "";
  state.composerDraft = "";
  renderAutonomy();
  renderProofOfWork();
  renderCEOAttention();
  applyNavigationLayout();
  renderComposerState(null);
}

function renderRequestSummary() {
  const record = state.record;
  if (!record) {
    ui.requestSummary.replaceChildren();
    return;
  }
  const children = [
    node("div", { class: "thread-summary-head" },
      node("h1", { class: "thread-title" }, requestTitleText(record.request)),
      isArchivedRecord(record) ? node("span", { class: "archived-badge" }, "削除済み") : null,
    ),
  ];
  if (isArchivedRecord(record)) {
    children.push(node("div", { class: "archived-actions" },
      button("元に戻す", "quiet chip", confirmUnarchiveSession),
    ));
  }
  ui.requestSummary.replaceChildren(...children.filter(Boolean));
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

function avatarVariant(employeeID) {
  let hash = 0;
  const source = String(employeeID || "unknown");
  for (let index = 0; index < source.length; index += 1) {
    hash = ((hash << 5) - hash) + source.charCodeAt(index);
    hash |= 0;
  }
  return Math.abs(hash) % 6;
}

function avatarNode(employeeID, statusClass = "") {
  const variant = avatarVariant(employeeID);
  return node("span", { class: `employee-avatar avatar-v${variant} ${statusClass}`.trim(), "aria-hidden": "true" });
}

function employeeStatusIcon(displayStatus) {
  switch (displayStatus) {
  case "作業中": return "✎";
  case "レビュー中": return "✓";
  case "修正中": return "↺";
  case "社長と相談中": return "💬";
  case "完了": return "◦";
  default: return "•";
  }
}

function employeePoseClass(displayStatus) {
  switch (displayStatus) {
  case "作業中": return "pose-desk";
  case "レビュー中": return "pose-review";
  case "修正中": return "pose-revise";
  case "社長と相談中": return "pose-consult";
  case "完了": return "pose-done";
  default: return "pose-idle";
  }
}

function characterZoneClass(employee, index) {
  switch (employee.display_status) {
  case "レビュー中": return "zone-review";
  case "社長と相談中": return "zone-consult";
  case "作業中":
  case "修正中":
  case "完了":
    return `zone-desk-${index}`;
  default:
    return `zone-walk-${index}`;
  }
}

function officeRoomSvgNode() {
  const svgNS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(svgNS, "svg");
  svg.setAttribute("class", "office-room-svg");
  svg.setAttribute("viewBox", "0 0 400 240");
  svg.setAttribute("aria-hidden", "true");

  const defs = document.createElementNS(svgNS, "defs");
  const wallGrad = document.createElementNS(svgNS, "linearGradient");
  wallGrad.setAttribute("id", "room-wall");
  wallGrad.setAttribute("x1", "0");
  wallGrad.setAttribute("y1", "0");
  wallGrad.setAttribute("x2", "0");
  wallGrad.setAttribute("y2", "1");
  for (const [offset, color] of [["0%", "var(--room-wall-top, #eef3ef)"], ["100%", "var(--room-wall-bottom, #dfe8e1)"]]) {
    const stop = document.createElementNS(svgNS, "stop");
    stop.setAttribute("offset", offset);
    stop.setAttribute("stop-color", color);
    wallGrad.append(stop);
  }
  const floorGrad = document.createElementNS(svgNS, "linearGradient");
  floorGrad.setAttribute("id", "room-floor");
  floorGrad.setAttribute("x1", "0");
  floorGrad.setAttribute("y1", "0");
  floorGrad.setAttribute("x2", "0");
  floorGrad.setAttribute("y2", "1");
  for (const [offset, color] of [["0%", "var(--room-floor-top, #d8e0da)"], ["100%", "var(--room-floor-bottom, #c8d2cb)"]]) {
    const stop = document.createElementNS(svgNS, "stop");
    stop.setAttribute("offset", offset);
    stop.setAttribute("stop-color", color);
    floorGrad.append(stop);
  }
  defs.append(wallGrad, floorGrad);
  svg.append(defs);

  const wall = document.createElementNS(svgNS, "rect");
  wall.setAttribute("class", "room-wall");
  wall.setAttribute("x", "0");
  wall.setAttribute("y", "0");
  wall.setAttribute("width", "400");
  wall.setAttribute("height", "170");
  wall.setAttribute("fill", "url(#room-wall)");
  const floor = document.createElementNS(svgNS, "rect");
  floor.setAttribute("class", "room-floor");
  floor.setAttribute("x", "0");
  floor.setAttribute("y", "170");
  floor.setAttribute("width", "400");
  floor.setAttribute("height", "70");
  floor.setAttribute("fill", "url(#room-floor)");
  svg.append(wall, floor);

  const addRect = (className, attrs) => {
    const rect = document.createElementNS(svgNS, "rect");
    rect.setAttribute("class", className);
    for (const [key, value] of Object.entries(attrs)) rect.setAttribute(key, String(value));
    svg.append(rect);
    return rect;
  };
  addRect("room-window", { x: 288, y: 28, width: 72, height: 48, rx: 4 });
  addRect("room-whiteboard", { x: 156, y: 34, width: 88, height: 52, rx: 4 });
  addRect("room-shelf", { x: 12, y: 72, width: 36, height: 56, rx: 3 });
  addRect("room-shelf-board", { x: 16, y: 78, width: 28, height: 4, rx: 1 });
  addRect("room-shelf-board", { x: 16, y: 92, width: 28, height: 4, rx: 1 });
  addRect("room-shelf-board", { x: 16, y: 106, width: 28, height: 4, rx: 1 });
  addRect("room-consult-rug", { x: 18, y: 176, width: 56, height: 24, rx: 8 });
  const windowBarV = document.createElementNS(svgNS, "line");
  windowBarV.setAttribute("class", "room-window-bar");
  windowBarV.setAttribute("x1", "324");
  windowBarV.setAttribute("y1", "32");
  windowBarV.setAttribute("x2", "324");
  windowBarV.setAttribute("y2", "72");
  const windowBarH = document.createElementNS(svgNS, "line");
  windowBarH.setAttribute("class", "room-window-bar");
  windowBarH.setAttribute("x1", "292");
  windowBarH.setAttribute("y1", "52");
  windowBarH.setAttribute("x2", "356");
  windowBarH.setAttribute("y2", "52");
  svg.append(windowBarV, windowBarH);
  const line = document.createElementNS(svgNS, "line");
  line.setAttribute("class", "room-board-line");
  line.setAttribute("x1", "168");
  line.setAttribute("y1", "50");
  line.setAttribute("x2", "228");
  line.setAttribute("y2", "50");
  svg.append(line);
  addRect("room-desk room-desk-left", { x: 44, y: 148, width: 72, height: 10, rx: 3 });
  addRect("room-monitor room-monitor-left", { x: 68, y: 124, width: 24, height: 18, rx: 2 });
  addRect("room-chair room-chair-left", { x: 72, y: 158, width: 16, height: 10, rx: 3 });
  addRect("room-desk room-desk-center", { x: 164, y: 148, width: 72, height: 10, rx: 3 });
  addRect("room-monitor room-monitor-center", { x: 188, y: 124, width: 24, height: 18, rx: 2 });
  addRect("room-chair room-chair-center", { x: 192, y: 158, width: 16, height: 10, rx: 3 });
  addRect("room-desk room-desk-review", { x: 284, y: 148, width: 72, height: 10, rx: 3 });
  addRect("room-monitor room-monitor-review", { x: 308, y: 124, width: 24, height: 18, rx: 2 });
  addRect("room-chair room-chair-review", { x: 312, y: 158, width: 16, height: 10, rx: 3 });
  addRect("room-doc-stack", { x: 332, y: 132, width: 18, height: 14, rx: 2 });
  addRect("room-coffee", { x: 248, y: 176, width: 10, height: 12, rx: 2 });

  const pot = document.createElementNS(svgNS, "circle");
  pot.setAttribute("class", "room-plant-pot");
  pot.setAttribute("cx", "36");
  pot.setAttribute("cy", "182");
  pot.setAttribute("r", "8");
  addRect("room-plant-stem", { x: 34, y: 166, width: 4, height: 12, rx: 2 });
  const leafA = document.createElementNS(svgNS, "circle");
  leafA.setAttribute("class", "room-plant-leaf");
  leafA.setAttribute("cx", "32");
  leafA.setAttribute("cy", "162");
  leafA.setAttribute("r", "6");
  const leafB = document.createElementNS(svgNS, "circle");
  leafB.setAttribute("class", "room-plant-leaf");
  leafB.setAttribute("cx", "40");
  leafB.setAttribute("cy", "160");
  leafB.setAttribute("r", "5");
  svg.append(pot, leafA, leafB);

  return node("div", { class: "office-room-scene", "aria-hidden": "true" }, svg);
}

function roomCharacterNode(employee, index) {
  const variant = avatarVariant(employee.id);
  const statusClass = employeeStatusClass(employee.display_status);
  const poseClass = employeePoseClass(employee.display_status);
  const zoneClass = characterZoneClass(employee, index);
  return node("div", {
    class: `room-character avatar-v${variant} ${statusClass} ${poseClass} ${zoneClass}${employee.is_liaison ? " liaison" : ""}${employee.ceo_attention ? " attention" : ""}`.trim(),
    title: employee.current_work_title || employee.display_status || "",
  },
  node("div", { class: "room-character-figure", "aria-hidden": "true" },
    node("span", { class: "char-hair" }),
    node("span", { class: "char-head" },
      node("span", { class: "char-face" },
        node("span", { class: "char-eye char-eye-left" }),
        node("span", { class: "char-eye char-eye-right" }),
      ),
    ),
    node("span", { class: "char-torso" }),
    node("span", { class: "char-arm char-arm-left" }),
    node("span", { class: "char-arm char-arm-right" }),
    node("span", { class: "char-leg char-leg-left" }),
    node("span", { class: "char-leg char-leg-right" }),
    node("span", { class: "char-chair-back" }),
  ),
  node("span", { class: "char-shadow", "aria-hidden": "true" }),
  employee.display_status === "社長と相談中" || employee.ceo_attention
    ? node("span", { class: "char-speech-bubble", "aria-hidden": "true" })
    : null,
  );
}

function roomCharacterLabel(employee) {
  const statusClass = employeeStatusClass(employee.display_status);
  return node("div", { class: "room-character-label" },
    node("strong", {}, employee.name || employee.id),
    node("small", {}, `${employee.role || "役割未設定"} · ${employee.display_status || "待機中"}`),
    node("span", { class: `employee-status ${statusClass}`.trim() },
      node("span", { class: "status-glyph", "aria-hidden": "true" }, employeeStatusIcon(employee.display_status)),
      employee.display_status || "待機中",
    ),
    employee.current_work_title ? node("p", { class: "employee-task" }, employee.current_work_title) : null,
  );
}

function employeeCompactRow(employee) {
  const statusClass = employeeStatusClass(employee.display_status);
  const variant = avatarVariant(employee.id);
  return node("div", { class: "employee-compact-row" },
    node("span", { class: `employee-compact-avatar avatar-v${variant} ${statusClass}`.trim(), "aria-hidden": "true" },
      node("span", { class: "char-hair" }),
      node("span", { class: "char-head" }),
      node("span", { class: "char-torso" }),
    ),
    node("div", { class: "employee-compact-copy" },
      node("strong", {}, employee.name || employee.id),
      node("small", {}, `${employee.department || "会社"} · ${employee.role || "役割未設定"}`),
      node("span", { class: `employee-status ${statusClass}`.trim() },
        node("span", { class: "status-glyph", "aria-hidden": "true" }, employeeStatusIcon(employee.display_status)),
        node("span", { class: "status-dot", "aria-hidden": "true" }),
        employee.display_status || "待機中",
      ),
      employee.current_work_title ? node("p", { class: "employee-task" }, employee.current_work_title) : null,
    ),
  );
}

function renderEmployeesPane() {
  const employees = state.companyActivity?.employees || [];
  ui.teamCount.textContent = employees.length ? `${employees.length}人` : "";
  if (!employees.length) {
    ui.employeeGrid.replaceChildren(node("p", { class: "empty" }, "AI社員はまだ読み込まれていません。"));
  } else {
    ui.employeeGrid.replaceChildren(
      node("div", { class: "office-room" },
        officeRoomSvgNode(),
        node("div", { class: "office-room-characters", "aria-hidden": "true" },
          ...employees.map((employee, index) => roomCharacterNode(employee, index)),
        ),
        node("div", { class: "office-room-labels" },
          ...employees.map((employee) => roomCharacterLabel(employee)),
        ),
        node("div", { class: "office-room-compact" },
          ...employees.map((employee) => employeeCompactRow(employee)),
        ),
      ),
    );
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
	  workspace.organization_ready && state.providerStatus?.configured ? button("会社を始める", "primary", () => { ui.setupDialog.close(); openNewRequestDraft(); }) : null,
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

function openNewRequestDraft() {
  closeNavDrawer();
  state.draftRequest = { sessionID: sessionID(), text: "" };
  state.record = null;
  state.next = null;
  state.lastError = null;
  clearSessionPresentationState();
  state.pendingStart = null;
  localStorage.removeItem(STORAGE_SESSION);
  clearActionSurface();
  ui.composerInput.value = "";
  setNav("request_detail");
  renderDraftRequestDetail();
}

async function submitDraftRequest() {
  if (!isDraftRequestActive() || submitDraftRequest.inFlight) {
    if (submitDraftRequest.inFlight) toast("同じ処理を実行中です");
    return;
  }
  const request = ui.composerInput.value.trim();
  if (!request) return toast("依頼内容を入力してください。");
  submitDraftRequest.inFlight = true;
  const input = { version: INTERACTION_VERSION, session_id: state.draftRequest.sessionID, request, current_time: now() };
  try {
    setBusy(true, "依頼内容を確認しています", "まだWorkspaceやProviderは変更しません。");
    const plan = await requestJSON("/v1/interaction-plans", { method: "POST", body: JSON.stringify(input) });
    setBusy(false);
    localStorage.setItem(STORAGE_SESSION, input.session_id);
    const completed = await executeNextCommand({ operation: "interaction.start" }, {
      session_id: input.session_id,
      request,
      request_digest: plan.session.request_digest,
      model: plan.session.model,
      current_time: input.current_time,
    }, "依頼を保存しています", "Sessionを作成しています。", commandID());
    state.draftRequest = null;
    ui.composerInput.value = "";
    if (completed) {
      await selectSession(input.session_id);
      showRequestDetail(input.session_id);
    } else {
      applyNavigationLayout();
      if (state.lastError) {
        renderTimeline();
        renderComposerState(state.next);
      }
    }
  } catch (error) {
    showError(error, "依頼内容を確認できませんでした");
  } finally {
    submitDraftRequest.inFlight = false;
  }
}
submitDraftRequest.inFlight = false;

function closeDialog(event) {
  const dialog = event.currentTarget.closest("dialog");
  if (dialog?.open) dialog.close();
}

ui.pairingForm.addEventListener("submit", pair);
document.querySelector("#new-request-button")?.addEventListener("click", openNewRequestDraft);
document.querySelector("#refresh-button").addEventListener("click", () => refreshCurrent());
ui.sessionFilterActive?.addEventListener("click", () => setSessionListFilter("active"));
ui.sessionFilterArchived?.addEventListener("click", () => setSessionListFilter("archived"));
document.addEventListener("click", (event) => {
  if (!state.sessionMenuSessionId && !state.sessionConfirmSessionId) return;
  if (event.target.closest(".session-row")) return;
  closeSessionMenus();
  renderSessions();
});
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  if (!state.sessionMenuSessionId && !state.sessionConfirmSessionId) return;
  closeSessionMenus();
  renderSessions();
});
ui.settingsButton.addEventListener("click", openSettingsDialog);
document.querySelector("#provider-status-refresh").addEventListener("click", refreshProviderStatus);
ui.menuButton.addEventListener("click", openNavDrawer);
ui.navBackdrop.addEventListener("click", closeNavDrawer);
ui.navEmployeesHome.addEventListener("click", showEmployeesHome);
ui.navRequestList.addEventListener("click", showRequestList);
ui.navNewRequest.addEventListener("click", () => { closeNavDrawer(); openNewRequestDraft(); });
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
if (ui.composerInput) {
  ui.composerInput.addEventListener("input", () => {
    if (ui.threadComposer?.dataset.mode === "clarification") {
      state.composerDraft = ui.composerInput.value;
    }
  });
}
if (ui.threadComposer) {
  ui.threadComposer.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (isDraftRequestActive()) {
      await submitDraftRequest();
      return;
    }
    const next = state.next;
    if (!next || next.kind !== "answer_clarifications") return;
    await submitClarificationAnswers(next);
  });
}
document.querySelectorAll("[data-close-dialog]").forEach((control) => control.addEventListener("click", closeDialog));
window.matchMedia(DESKTOP_QUERY).addEventListener("change", () => {
  syncRequestDetailNavigation();
  applyNavigationLayout();
});

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
    if (anyPendingCommands().length) await resumeAllPendingCommands();
  } catch (error) {
    setConnected(false);
    ui.pairingView.hidden = false;
    toast(error.message);
  }
}

setInterval(async () => {
  if (!shouldBlockPollingRefresh() &&
    !ui.actionDialog.open && !ui.requestDialog.open && !ui.setupDialog.open && document.visibilityState === "visible") {
    if (isDraftRequestActive()) return;
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
