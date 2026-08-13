const INTERACTION_VERSION = "workspace-interaction.v1";
const COMMAND_VERSION = "workspace-command.v1";
const STORAGE_SESSION = "workcairn.active-session";
const STORAGE_PENDING = "workcairn.pending-command";
const STORAGE_VIEW = "workcairn.active-view";
const STORAGE_ERROR_PREFIX = "workcairn.last-error.";

const ui = {
  pairingView: document.querySelector("#pairing-view"),
  pairingForm: document.querySelector("#pairing-form"),
  workspaceView: document.querySelector("#workspace-view"),
  myActionsView: document.querySelector("#my-actions-view"),
  companyView: document.querySelector("#company-view"),
  myActionsTab: document.querySelector("#my-actions-tab"),
  companyTab: document.querySelector("#company-tab"),
  status: document.querySelector("#connection-status"),
  backgroundStatus: document.querySelector("#background-status"),
  settingsButton: document.querySelector("#settings-button"),
  activeCard: document.querySelector("#active-card"),
  detailsPanel: document.querySelector("#details-panel"),
  details: document.querySelector("#details-content"),
  sessionList: document.querySelector("#session-list"),
  timeline: document.querySelector("#activity-timeline"),
  companyTimeline: document.querySelector("#company-timeline"),
  companyStatus: document.querySelector("#company-status"),
  employeeGrid: document.querySelector("#employee-grid"),
  teamCount: document.querySelector("#team-count"),
  companyFlow: document.querySelector("#company-flow"),
  attentionGrid: document.querySelector("#attention-grid"),
  autonomySummary: document.querySelector("#autonomy-summary"),
  proofOfWork: document.querySelector("#proof-of-work"),
  requestDialog: document.querySelector("#request-dialog"),
  requestForm: document.querySelector("#request-form"),
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
  evidence: new Map(),
  workReport: null,
  workReportError: null,
  providerStatus: null,
  workspaceStatus: null,
	localSetupAvailable: false,
  lastError: null,
  renderKey: "",
  detailRenderKey: "",
  timelineRenderKey: "",
  activeCommandID: "",
  busy: false,
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
    completed: "仕事完了",
    action_completed: "公開完了",
    action_attention_required: "公開確認が必要",
    waiting: "待機中",
    standby: "必要時に参加",
    blocked: "確認が必要",
  };
  return labels[value] || value || "確認中";
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
    throw new APIError(String(code), response.status, payload?.error || payload);
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
    recovery_required: Boolean(detail?.recovery_required),
    at: now(),
  };
  try {
    const existing = JSON.parse(localStorage.getItem(errorStorageKey(sessionID)) || "null");
    if (existing && existing.session_version === snapshot.session_version && existing.title === snapshot.title &&
        existing.code === snapshot.code && existing.stage === snapshot.stage && existing.command_id === snapshot.command_id &&
        existing.request_id === snapshot.request_id && existing.recovery_required === snapshot.recovery_required) {
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

async function copySanitizedError(error) {
  const detail = [
    `Error code: ${error.code || "UNKNOWN_ERROR"}`,
    `Stage: ${error.stage || "—"}`,
    `Command ID: ${error.command_id || "—"}`,
    `Request ID: ${error.request_id || "—"}`,
  ].join("\n");
  try {
    await navigator.clipboard.writeText(detail);
    toast("エラー詳細をコピーしました。");
  } catch {
    toast("コピーできませんでした。詳細を長押ししてコピーしてください。");
  }
}

function setView(name, remember = true) {
  const company = name === "company";
  ui.myActionsView.hidden = company;
  ui.companyView.hidden = !company;
  ui.myActionsTab.classList.toggle("active", !company);
  ui.companyTab.classList.toggle("active", company);
  ui.myActionsTab.setAttribute("aria-selected", String(!company));
  ui.companyTab.setAttribute("aria-selected", String(company));
  if (remember) localStorage.setItem(STORAGE_VIEW, company ? "company" : "actions");
  if (company) refreshCompanyView();
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
  const planResponseInvalid = code === "INTERACTION_PLAN_FAILED" && stage === "ceo_plan_parser";
  const workflowAssignmentRequired = code === "WORKFLOW_TASK_ASSIGNMENT_REQUIRED";
  const workflowReviewerRequired = code === "WORKFLOW_REVIEWER_ASSIGNMENT_REQUIRED";
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
  const providerIssue = providerSetupRequired || providerGenerationFailed || Boolean(providerFailureCopy);
  const providerRequestID = detail?.provider_failure?.request_id;
  const providerSettingsAction = providerSetupRequired || code === "PROVIDER_AUTHENTICATION_REQUIRED" || code === "PROVIDER_PERMISSION_DENIED";
  const pending = sessionStorage.getItem(STORAGE_PENDING);
  const errorRenderKey = `error:${JSON.stringify([remembered?.session_id || "", remembered?.session_version || 0, code, stage || "", remembered?.command_id || ""] )}`;
  if (state.renderKey === errorRenderKey) return;
  state.renderKey = errorRenderKey;
  ui.activeCard.className = "action-card attention";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "確認が必要です"),
    node("h2", {}, title),
    node("p", { class: "lead" }, providerSetupRequired
      ? "AIサービスの接続設定が不足しています。Providerへ依頼は送信されていません。MacのAI Connectionsから接続してください。"
      : providerFailureCopy
        ? providerFailureCopy
      : planResponseInvalid
        ? "AIサービスから応答を受信しましたが、安全な進め方として確認できる形式ではありませんでした。進め方は保存・適用されていません。"
      : workflowAssignmentRequired
        ? "担当AIを決められない仕事があるため、実行を開始していません。Organizationを確認してから、改めて実行内容を確認してください。"
      : workflowReviewerRequired
        ? "Makerと異なるReviewerを一意に決められなかったため、Workflowを開始していません。OrganizationのQA Engineerを確認してください。"
      : providerGenerationFailed
        ? "AIサービスで進め方を生成できませんでした。自動retryや別サービスへの切替は行っていません。接続状態を確認してください。"
      : "成立済みの記録を推測で変更せず、現在の状態を確認してください。"),
    node("div", { class: "error-box" },
      node("strong", {}, code),
      stage ? node("div", {}, `stage: ${stage}`) : null,
      providerRequestID ? node("div", {}, `問い合わせID: ${providerRequestID}`) : null,
    ),
    node("div", { class: "button-row" },
      button(providerSettingsAction ? "AI Connectionsを開く" : (providerIssue ? "進め方の作成待ちへ戻る" : (pending ? "Command状態を再確認" : "状態を再確認")), "primary", () => providerSettingsAction ? openSettingsDialog() : (pending ? resumePendingCommand(pending) : refreshCurrent())),
      button("依頼一覧へ", "quiet", () => selectSession(null)),
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
  setConnected(true);
  await Promise.all([loadSessions(), loadProviderStatus(), loadWorkspaceStatus(), loadOrganization().catch(() => null)]);
  const stored = localStorage.getItem(STORAGE_SESSION);
  const candidate = state.sessions.find((record) => record.session_id === stored) ||
    state.sessions.find((record) => !["completed", "action_completed"].includes(record.state)) || state.sessions[0];
  await selectSession(candidate?.session_id || null);
  const preferredView = localStorage.getItem(STORAGE_VIEW);
  setView(preferredView || (window.matchMedia("(min-width: 900px)").matches ? "company" : "actions"), false);
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
	setBusy(true, "MacでClaudeへ接続しています", "Macに表示される安全な入力画面を確認してください。secretはbrowserへ送信しません。");
	try {
		state.providerStatus = await requestJSON("/v1/local-setup/claude", { method: "POST", body: "{}" });
		setBusy(false);
		renderProviderSettings();
		if (state.workspaceStatus?.organization_ready) {
			if (ui.settingsDialog.open) ui.settingsDialog.close();
			openSetupWizard();
		} else if (ui.setupDialog.open) renderSetupWizard();
		toast("Claudeへ接続しました。RoutingはAutomaticです。");
	} catch (error) {
		setBusy(false);
		toast(error.status === 403 ? "AI ConnectionはMac本体の画面から設定してください。" : "Claude接続は完了していません。Macの入力画面を確認してください。");
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
  );
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

async function selectSession(id) {
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
    renderDetails();
    renderCompany();
    return;
  }
  localStorage.setItem(STORAGE_SESSION, id);
  state.renderKey = "";
  state.detailRenderKey = "";
  state.timelineRenderKey = "";
  await refreshCurrent();
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
    setConnected(true);
    renderNext();
    renderDetails();
    renderCompany();
    renderTimeline();
    await loadSessions();
  } catch (error) {
    setConnected(false);
    showError(error, silent ? "Macとの接続を確認してください" : "依頼の状態を取得できませんでした");
  }
}

function renderEmpty() {
  ui.activeCard.className = "action-card complete";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "準備できています"),
    node("h2", {}, "会社に新しい仕事を依頼してください"),
    node("p", { class: "lead" }, "依頼した後はAI社員が計画・実行・レビューを進め、必要な質問と承認だけをここに表示します。"),
    node("div", { class: "button-row" }, button("仕事を依頼する", "primary", openRequestDialog)),
  );
  renderTimeline();
}

function renderSessions() {
  if (!state.sessions.length) {
    ui.sessionList.replaceChildren(node("p", { class: "empty" }, "まだ依頼はありません。"));
    return;
  }
  ui.sessionList.replaceChildren(...state.sessions.map((record) => {
    let hasError = false;
    try { hasError = Boolean(JSON.parse(localStorage.getItem(errorStorageKey(record.session_id)) || "null")); } catch {}
    return node("button", { class: "session-item", type: "button", onclick: () => selectSession(record.session_id) },
      node("span", {},
        node("strong", {}, record.request),
        node("small", {}, new Date(record.created_at).toLocaleString("ja-JP")),
      ),
      node("span", { class: `state-chip${hasError ? " error" : ""}` }, hasError ? "確認が必要" : stateLabel(record.state)),
    );
  }));
}

function renderNext(force = false) {
  const next = state.next;
  if (!next) return renderEmpty();
  const key = JSON.stringify([next.session_id, next.expected_version, next.kind, state.lastError?.code || "", state.lastError?.stage || ""]);
  if (!force && state.renderKey === key) return;
  state.renderKey = key;
  if (state.lastError) return renderRememberedError(state.lastError);
  ui.activeCard.className = "action-card";
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
  ui.activeCard.className = "action-card attention";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, error.recovery_required ? "Recoveryが必要です" : "確認が必要です"),
    node("h2", {}, error.title || "処理を完了できませんでした"),
    node("p", { class: "lead" }, "エラーは消さずに保存しています。成立済みの仕事は保持し、自動retryや推測修復は行っていません。"),
    node("details", { class: "artifact-detail" },
      node("summary", {}, "詳細を見る"),
      approvalFacts([
        ["Error code", error.code], ["Stage", error.stage || "—"],
        ["Command ID", error.command_id || "未発行"], ["問い合わせID", error.request_id || "—"],
      ]),
    ),
    node("div", { class: "button-row" },
      button(error.command_id ? "Command状態を確認" : "状態を再確認", "primary", () => error.command_id
        ? inspectCommands([{ scope: "workspace", command_id: error.command_id }])
        : refreshCurrent()),
      button("詳細をコピー", "quiet", () => copySanitizedError(error)),
      button("新しい状態を確認", "quiet", async () => { clearCurrentError(); state.renderKey = ""; await refreshCurrent(); }),
    ),
  );
}

function renderPlanGeneration(next) {
  if (!state.providerStatus?.configured) return renderProviderSetup();
  const hasAnswers = state.record.turns.some((turn) => turn.kind === "clarification_answered");
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, hasAnswers ? "回答を反映" : "最初の確認"),
    node("h2", {}, hasAnswers ? "回答をもとに進め方を作り直します" : "仕事の進め方を作成します"),
    node("p", { class: "lead" }, "AIはまだ会社データを変更しません。依頼内容を整理し、必要な質問と進め方を作成します。"),
    approvalFacts([
      ["依頼", state.record.request],
      ["AIサービス", "WorkCairnが接続設定から選択"],
      ["Session", state.record.session_id],
    ]),
    node("div", { class: "button-row" },
      button("進め方の作成を承認", "primary", () => executeNextCommand(next, {
        session_id: next.session_id,
        expected_version: next.expected_version,
        current_time: now(),
      }, "進め方を作成しています", "質問または仕事の進め方ができるまでお待ちください。")),
      button("今は承認しない", "quiet", () => toast("変更せず、承認待ちのまま保存されています。")),
    ),
  );
}

function renderProviderSetup() {
  const missing = state.providerStatus?.missing || [];
  const invalid = state.providerStatus?.invalid || [];
  const reasons = [];
  if (missing.includes("credential")) reasons.push("Claudeがまだ接続されていません");
  if (invalid.length) reasons.push("Provider設定を安全に検証できませんでした");
  if (!reasons.length) reasons.push("接続状態を取得できませんでした");
  ui.activeCard.className = "action-card attention";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "Mac側の設定が必要です"),
    node("h2", {}, "AIサービスへ接続してください"),
    node("p", { class: "lead" }, "この依頼は保存済みです。設定が整うまでAIサービスへ送信せず、進め方の作成を開始しません。"),
    node("ul", { class: "trust-list" }, ...reasons.map((reason) => node("li", {}, reason))),
    node("p", { class: "supporting" }, "MacのAI ConnectionsからClaudeを接続してください。iPhoneからsecretは送らず、別Providerへの自動fallbackも行いません。"),
    node("div", { class: "button-row" },
      button("AI Connectionsを開く", "primary", openSettingsDialog),
      button("今は設定しない", "quiet", () => toast("依頼は進め方の作成待ちのまま保存されています。")),
    ),
  );
}

function renderQuestions(next) {
  const clarificationKey = JSON.stringify([next.session_id, next.expected_version, next.questions]);
  const currentForm = ui.activeCard.querySelector("form.question-list[data-clarification-key]");
  if (currentForm?.dataset.clarificationKey === clarificationKey) return;
  const form = node("form", { class: "question-list", dataset: { clarificationKey } });
  for (const [index, question] of next.questions.entries()) {
    form.append(node("label", { class: "question-card" },
      node("p", {}, question),
      node("textarea", { name: `answer-${index}`, rows: "3", required: true, placeholder: "回答を入力" }),
    ));
  }
  form.append(node("div", { class: "button-row" },
    button("回答を送信", "primary", null, "submit"),
    button("後で回答する", "quiet", () => toast("質問は回答待ちのまま保存されています。")),
  ));
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const data = new FormData(form);
    const answers = next.questions.map((question, index) => ({ question, answer: data.get(`answer-${index}`)?.toString().trim() || "" }));
    if (answers.some((answer) => !answer.answer)) return toast("すべての質問に回答してください。");
    executeNextCommand(next, { session_id: next.session_id, expected_version: next.expected_version, answers, current_time: now() }, "回答を保存しています", "保存後に次の進め方の作成確認へ進みます。");
  });
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, `${next.questions.length}件の質問`),
    node("h2", {}, "確認したいことがあります"),
    node("p", { class: "lead" }, "仕事を始める前に必要なことだけ回答してください。"),
    form,
  );
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
  const action = task.required_role === "QA Engineer" ? "内容を確認します" : task.title;
  return `${index + 1}. ${roleLabel(task.required_role)}が${action}`;
}

function renderPlanApproval(next) {
  const current = currentPlan();
  if (!current) return showError(new Error("Plan evidence is missing"));
  const identifier = localStorage.getItem(`workcairn.project.${state.record.session_id}`) || projectID();
  localStorage.setItem(`workcairn.project.${state.record.session_id}`, identifier);
  const projectInput = node("input", { id: "project-id", value: identifier, required: true, autocomplete: "off", spellcheck: "false" });
  const tasks = node("ol", { class: "public-plan" }, ...current.plan.proposed_tasks.map((task, index) => node("li", {}, planStepCopy(task, index).replace(/^\d+\. /, ""))));
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "進め方の確認"),
    node("h2", {}, current.plan.project_name),
    node("p", { class: "lead" }, current.plan.summary),
    node("div", { class: "approval-box" },
      node("strong", {}, "このように進めます"),
      tasks,
      node("details", { class: "technical-details" },
        node("summary", {}, "技術的な詳細を見る"),
        node("label", { for: "project-id" }, "Project ID"), projectInput,
        approvalFacts([["Plan digest", shortDigest(current.digest)], ["Task数", String(current.plan.proposed_tasks.length)]]),
      ),
    ),
    node("div", { class: "button-row" },
      button("この進め方で始める", "primary", () => {
        const value = projectInput.value.trim();
        if (!value) return toast("Project IDを入力してください。");
        localStorage.setItem(`workcairn.project.${state.record.session_id}`, value);
        executeNextCommand(next, {
          session_id: next.session_id, expected_version: next.expected_version,
          project_id: value, plan_digest: current.digest, current_time: now(),
        }, "Projectを作成しています", "ProjectとTaskを安全な順序で保存しています。");
      }),
      button("承認しない", "quiet", () => toast("Workspaceは変更されていません。")),
    ),
  );
}

async function loadOrganization(force = false) {
  if (force || !state.organization) state.organization = await requestJSON("/v1/organization");
  return state.organization;
}

async function renderWorkflowApproval(next) {
  const workflowFormKey = JSON.stringify([next.session_id, next.expected_version]);
  const currentForm = ui.activeCard.querySelector("form.stack-form[data-workflow-form-key]");
  if (currentForm?.dataset.workflowFormKey === workflowFormKey) return;
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "実行準備"),
    node("h2", {}, "担当AIに仕事を開始させます"),
    node("p", { class: "lead" }, "担当AIが順番に仕事を実行し、Reviewerの指摘があれば修正と再確認まで進めます。"),
  );
  const form = node("form", { class: "stack-form", dataset: { workflowFormKey } });
  const maxTasks = node("input", { id: "max-tasks", name: "max_tasks", type: "number", min: "1", max: "100", value: "20", inputmode: "numeric", required: true });
  form.append(
    node("p", { class: "empty" }, "Makerとは別のQA Reviewerを、役割と許可範囲から自動選択します。"),
    node("label", { for: "max-tasks" }, "今回任せる仕事ステップの上限"), maxTasks,
    node("div", { class: "button-row" },
      button("実行内容を確認", "primary", null, "submit"),
      button("今は実行しない", "quiet", () => toast("仕事は開始されていません。")),
    ),
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
  ui.activeCard.append(form);
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
    showApprovalSheet({
      title: "会社にこの仕事を任せますか？",
      description: "承認後は担当AIが成果物を作り、別のReviewerが確認し、必要な修正まで上限内で進めます。",
      facts: [
        ["仕事", plan.project_name],
        ["レビュー担当", plan.reviewer_name],
        ["任せる上限", `${plan.autonomy_contract.execution_limit}件の仕事ステップ`],
        ["任せるAI社員", plan.autonomy_contract.allowed_employee_ids.map(employeeLabel).join(" / ")],
        ["安全条件", "全成果物をReview・Revisionは委任・外部公開は別承認・支出は禁止"],
      ],
      technicalFacts: [
        ["Project ID", plan.project_id], ["Reviewer ID", plan.reviewer_id],
        ["Next Task", plan.next?.task_id || "readinessに従う"], ["Plan digest", plan.workflow_plan_digest],
      ],
      approveLabel: "承認して実行",
      onApprove: () => executeNextCommand(next, {
        session_id: next.session_id, expected_version: next.expected_version,
        reviewer_id: plan.reviewer_id, current_time: currentTime, max_tasks: maxTasks,
        autonomy_contract: plan.autonomy_contract,
        workflow_plan_digest: plan.workflow_plan_digest,
        approval_reference: `mobile-ui:${next.session_id}:v${next.expected_version}`,
      }, "Workflowを実行しています", "Task、Review、必要なRevisionを順番に進めています。"),
    });
  } catch (error) {
    showError(error, "実行内容を確認できませんでした");
  }
}

function renderCompletion(next) {
  ui.activeCard.className = "action-card complete";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "仕事が完了しました"),
    node("h2", {}, "すべての仕事とReviewが完了しています"),
    node("p", { class: "lead" }, "成果物はWorkspaceに保存されています。必要な場合だけ、別の承認で外部公開できます。"),
    node("div", { class: "button-row" },
      button("完了を確認", "primary", () => toast("この依頼は完了しています。")),
      button("WordPressへ公開", "", () => openActionSheet(next)),
    ),
  );
}

function renderDone() {
  const action = [...state.record.turns].reverse().find((turn) => turn.action)?.action;
  ui.activeCard.className = "action-card complete";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "完了"),
    node("h2", {}, "依頼した仕事が完了しました"),
    node("p", { class: "lead" }, action?.publication?.url ? "成果物の外部公開まで完了しています。" : "成果物はWorkspaceに保存されています。"),
    action?.publication?.url ? node("a", { class: "button primary", href: action.publication.url, target: "_blank", rel: "noreferrer" }, "公開先を開く") : null,
    node("div", { class: "button-row" }, button("新しい仕事を依頼", "", openRequestDialog)),
  );
}

function renderAttention(next, title) {
  ui.activeCard.className = "action-card attention";
  const references = (next.commands || []).map((reference) =>
    node("div", {}, node("dt", {}, reference.scope === "workspace" ? "Workspace command" : "Project command"), node("dd", { class: "digest" }, reference.command_id)),
  );
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "自動継続を停止しました"),
    node("h2", {}, title),
    node("p", { class: "lead" }, "成立済みの成果物や記録は保持されています。自動retryやrollbackをせず、CommandとRecovery evidenceを確認してください。"),
    node("div", { class: "approval-box" }, node("dl", {}, ...references)),
    node("div", { class: "button-row" },
      button("Command状態を確認", "primary", () => inspectCommands(next.commands || [])),
      button("Sessionを再確認", "quiet", () => refreshCurrent()),
    ),
  );
}

async function inspectCommands(references) {
  setBusy(true, "記録を確認しています", "Command Ledgerをread-onlyで取得しています。");
  try {
    const results = await Promise.all(references.map((reference) => {
      const query = new URLSearchParams({ scope: reference.scope });
      if (reference.project_name) query.set("project", reference.project_name);
      return requestJSON(`/v1/commands/${encodeURIComponent(reference.command_id)}?${query}`);
    }));
    setBusy(false);
    showApprovalSheet({
      title: "Command Ledger",
      description: "自動回復は行いません。partialまたはrunningの場合はMacのRecovery手順でcanonical evidenceを確認してください。",
      facts: results.map((result, index) => [references[index].command_id, `${result.state}${result.failure?.stage ? ` / ${result.failure.stage}` : ""}`]),
      approveLabel: "閉じる",
      approveKind: "quiet",
      onApprove: () => {},
      hideCancel: true,
    });
  } catch (error) {
    showError(error, "Command状態を取得できませんでした");
  }
}

function openActionSheet(next) {
  const tasks = next.eligible_task_ids || [];
  const taskSelect = node("select", { id: "action-task", name: "task_id", required: true }, ...tasks.map((task) => node("option", { value: task }, task)));
  const target = node("input", { id: "action-target", name: "target_id", placeholder: "例：site-main", required: true });
  ui.actionForm.replaceChildren(
    node("div", { class: "sheet-handle" }),
    node("div", { class: "sheet-heading" },
      node("div", {}, node("p", { class: "eyebrow" }, "EXTERNAL ACTION"), node("h2", {}, "WordPressへ公開")),
      node("button", { class: "icon-button", type: "button", "aria-label": "閉じる", onclick: closeActionDialog }, "×"),
    ),
    node("p", { class: "supporting" }, "公開は別の外部副作用です。対象成果物と公開先を確認した後、もう一度明示承認します。"),
    node("label", { for: "action-task" }, "公開するTask"), taskSelect,
    node("label", { for: "action-target" }, "公開先ID"), target,
    node("div", { class: "sheet-actions" },
      button("キャンセル", "quiet", closeActionDialog),
      button("公開内容を確認", "primary", null, "submit"),
    ),
  );
  ui.actionForm.onsubmit = async (event) => {
    event.preventDefault();
    const taskID = taskSelect.value;
    const targetID = target.value.trim();
    if (!taskID || !targetID) return toast("Taskと公開先IDを入力してください。");
    closeActionDialog();
    await prepareActionApproval(next, taskID, targetID);
  };
  ui.actionDialog.showModal();
}

async function prepareActionApproval(next, taskID, targetID) {
  const currentTime = now();
  try {
    const outerCommandID = commandID();
    setBusy(true, "公開内容を確認しています", "成果物digestと公開対象をread-onlyで検証しています。");
    const plan = await requestJSON("/v1/interaction-action-plans", {
      method: "POST",
      body: JSON.stringify({ version: INTERACTION_VERSION, session_id: next.session_id, expected_version: next.expected_version, task_id: taskID, target_id: targetID, current_time: currentTime, command_id: outerCommandID }),
    });
    setBusy(false);
    showApprovalSheet({
      title: "外部公開を承認しますか？",
      description: "承認後、成果物をWordPressへ新規公開します。公開後の自動削除やrollbackは行いません。",
      danger: true,
      facts: [
        ["Project", plan.project_name], ["Task", plan.task_id], ["公開先", plan.target_id],
        ["成果物 digest", plan.source_sha256], ["Action digest", plan.action_plan_digest],
      ],
      approveLabel: "承認して公開",
      approveKind: "danger",
      onApprove: () => executeNextCommand(next, {
        session_id: next.session_id, expected_version: next.expected_version,
        task_id: taskID, target_id: targetID, current_time: currentTime,
        action_plan_digest: plan.action_plan_digest,
      }, "WordPressへ公開しています", "外部公開とimmutable evidenceの保存を行っています。", outerCommandID),
    });
  } catch (error) {
    showError(error, "公開内容を確認できませんでした");
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
    setBusy(true, busyTitle, busyMessage);
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
  }
}

function wait(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function monitorAcceptedCommand(command) {
  setBusy(false);
  setBackgroundWorking(true);
  state.activeCommandID = command.command_id;
  renderCompany();
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
      return record;
    }
    if (record.state === "failed" || record.state === "partial_failure") {
      sessionStorage.removeItem(STORAGE_PENDING);
	  setBackgroundWorking(false);
	  const providerFailure = record.result?.provider_failure;
      throw new APIError(record.failure?.code || "COMMAND_FAILED", 422, {
        code: record.failure?.code || "COMMAND_FAILED",
        stage: record.failure?.stage,
        recovery_required: record.state === "partial_failure",
		command_id: command.command_id,
		provider_failure: providerFailure,
      });
    }
    if (record.state !== "running") {
	  setBackgroundWorking(false);
      throw new APIError("COMMAND_LEDGER_INVALID", 500, { code: "COMMAND_LEDGER_INVALID", recovery_required: true });
    }
    await wait(1000);
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
  try {
    await monitorAcceptedCommand(command);
    await refreshCurrent();
    setBusy(false);
    toast("Macで完了した処理を反映しました。");
  } catch (error) {
    setBackgroundWorking(false);
    showError(error, "前回のCommand状態を確認できませんでした");
  }
}

function timelineEntries() {
  const record = state.record;
  if (!record) return [];
  const entries = [{ title: "依頼を受け付けました", description: record.request, at: record.created_at }];
  for (const turn of record.turns || []) {
    if (turn.kind === "plan_generated" && turn.plan) {
      entries.push({ title: "進め方を作成しました", description: `${turn.plan.proposed_tasks.length}つの仕事に整理しました。`, at: turn.at, detail: `Plan digest: ${turn.plan_digest}` });
    } else if (turn.kind === "clarification_answered") {
      entries.push({ title: "質問への回答を受け取りました", description: "回答をもとに進め方を更新しました。", at: turn.at });
    } else if (turn.kind === "plan_applied") {
      entries.push({ title: "進め方が承認されました", description: `${turn.project_name}を作成し、担当AIへ仕事を渡しました。`, at: turn.at, detail: `Project: ${turn.project_id}` });
    } else if (turn.workflow) {
      for (const task of turn.workflow.tasks || []) {
        entries.push({ title: task.targeted_revision ? "修正を完了しました" : "成果物を作成しました", description: `${task.task_id}の成果物を保存しました。`, at: turn.at, detail: `Execution: ${task.execution_command_id || "—"}` });
        if (task.verdict) entries.push({ title: task.verdict === "request_changes" ? "Reviewerが修正を依頼しました" : "Reviewerが承認しました", description: task.revision_task_id ? `修正Task ${task.revision_task_id}へ引き渡しました。` : `${task.task_id}のReviewが完了しました。`, at: turn.at, detail: `Review: ${task.review_command_id || "—"}` });
      }
      if (turn.workflow.failure) entries.push({ title: "自動継続を停止しました", description: "成立済みの仕事を保持し、確認を待っています。", at: turn.at, attention: true, detail: `${turn.workflow.failure.code} / ${turn.workflow.failure.stage}` });
    } else if (turn.action) {
      entries.push({ title: turn.action.status === "published" ? "外部公開が完了しました" : "外部Actionを停止しました", description: turn.action.status === "published" ? "承認済みの成果物を外部へ反映しました。" : "成立済み記録を保持し、確認を待っています。", at: turn.at, attention: turn.action.status !== "published", detail: turn.action.command_id || "" });
    }
  }
  if (state.lastError) entries.push({ title: state.lastError.title || "処理を停止しました", description: "自動retryせず、次の判断を待っています。", at: state.lastError.at, attention: true, detail: [state.lastError.code, state.lastError.stage, state.lastError.command_id, state.lastError.request_id].filter(Boolean).join("\n") });
  return entries;
}

function timelineNode(entry) {
  return node("article", { class: `timeline-entry${entry.attention ? " attention" : ""}` },
    node("span", { class: "timeline-dot", "aria-hidden": "true" }),
    node("div", { class: "timeline-copy" },
      node("strong", {}, entry.title), node("p", {}, entry.description),
      entry.at ? node("small", {}, new Date(entry.at).toLocaleString("ja-JP")) : null,
      entry.detail ? node("details", { class: "timeline-technical" }, node("summary", {}, "詳細を見る"), node("code", {}, entry.detail)) : null,
    ),
  );
}

function renderTimeline() {
  const entries = timelineEntries();
  const key = JSON.stringify(entries);
  if (state.timelineRenderKey === key) return;
  state.timelineRenderKey = key;
  ui.timeline.replaceChildren(...(entries.length ? entries.map(timelineNode) : [node("p", { class: "empty" }, "依頼すると、会社の動きがここに残ります。")]));
  ui.companyTimeline.replaceChildren(...(entries.length ? entries.map(timelineNode) : [node("p", { class: "empty" }, "依頼すると、会社の動きがここに残ります。")]));
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
    renderCompany();
  } catch (error) {
    toast(`成果物の詳細を取得できませんでした: ${error.message}`);
  }
}

async function refreshCompanyView() {
  renderCompany();
  try {
    await loadOrganization();
    await loadTaskEvidenceDetails();
    renderCompany();
  } catch (error) {
    ui.employeeGrid.replaceChildren(node("p", { class: "warning" }, "社員情報を読み込めませんでした。仕事の状態は推測せず、Mac側のOrganizationを確認してください。"));
  }
}

function renderCompany() {
  if (!ui.companyStatus) return;
  const next = state.next;
  const pending = Boolean(sessionStorage.getItem(STORAGE_PENDING));
  const requiresAction = Boolean(state.lastError) || (next && !["optional_external_action_or_done", "done"].includes(next.kind));
  const proofNeedsAttention = Boolean(state.workReportError) || Boolean(state.workReport &&
    ["completed", "action_completed"].includes(state.record?.state) && !state.workReport.proof_of_work?.fully_verified);
  let icon = "✓";
  let title = "Your company is ready.";
  let message = "仕事を依頼すると、担当AIとReviewerの流れがここに表示されます。";
  if (pending) {
    icon = "↻";
    title = "Your company is working. No action needed.";
    message = "承認済みの仕事をMac上で進めています。次に判断が必要になったらMy Actionsへ表示します。";
  } else if (requiresAction) {
    icon = "!";
    title = "Your decision is needed.";
    message = "会社は安全な境界で停止しています。My Actionsに必要な質問または承認があります。";
  } else if (proofNeedsAttention) {
    icon = "!";
    title = "Some work records need confirmation.";
    message = "成立済み部分は保持しています。Proof of Workで未確認の記録を確認してください。";
  } else if (state.record) {
    title = next?.kind === "done" ? "Work completed." : "Your company is working. No action needed.";
    message = next?.kind === "done"
      ? "依頼した仕事と外部Actionが完了しています。詳細は後から確認できます。"
      : "現在あなたが対応することはありません。成果物は保存済みで、外部Actionは別承認です。";
  }
  ui.companyStatus.className = `company-status${requiresAction || proofNeedsAttention ? " needs-action" : ""}`;
  ui.companyStatus.replaceChildren(
    node("span", { class: "company-status-icon", "aria-hidden": "true" }, icon),
    node("div", {}, node("strong", {}, title), node("p", {}, message)),
  );
  renderEmployees();
  renderCompanyFlow();
  renderCEOAttention();
  renderAutonomy();
  renderProofOfWork();
  renderTimeline();
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
    node("div", { class: "setup-actions" },
	  !workspace.organization_ready ? button("最初のAIチームを確認", "primary", () => { ui.setupDialog.close(); showApprovalSheet({
		title: "最小のAIチームを作成しますか？",
		description: "承認すると、このWorkCairn専用Vaultだけに企画・コンテンツ・QA担当を追加します。既存社員や個人Vaultは変更しません。",
		facts: (workspace.starter_organization || []).map((candidate) => [candidate.role, candidate.name]),
		approveLabel: "承認してセットアップ",
			onApprove: async () => {
				ui.setupDialog.close();
				const completed = await executeNextCommand({ operation: "workspace.setup" }, { current_time: now() }, "会社を準備しています", "専用VaultへStarter Organizationを安全に作成しています。", commandID());
				if (!completed) return;
				await Promise.all([loadWorkspaceStatus(), loadOrganization(true)]);
				renderCompany();
				if (!state.providerStatus?.configured) openSettingsDialog();
				else openSetupWizard();
			},
	  }); }) : null,
      !state.providerStatus?.configured ? button("AI Connectionsを確認", "primary", () => { ui.setupDialog.close(); openSettingsDialog(); }) : null,
	  workspace.organization_ready && state.providerStatus?.configured ? button("会社を始める", "primary", () => { ui.setupDialog.close(); setView("actions"); openRequestDialog(); }) : null,
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

function renderEmployees() {
  const employees = state.organization?.inventory?.employees || [];
  ui.teamCount.textContent = employees.length ? `${employees.length} people` : "";
  if (!employees.length) {
    ui.employeeGrid.replaceChildren(node("p", { class: "empty" }, "AI社員はまだ読み込まれていません。"));
    return;
  }
  const plan = state.record ? currentPlan() : null;
  const makerTasks = new Map();
  for (const task of plan?.plan?.proposed_tasks || []) {
    if (!task.assignee_id) continue;
    const list = makerTasks.get(task.assignee_id) || [];
    list.push(task.title);
    makerTasks.set(task.assignee_id, list);
  }
  const workflow = latestWorkflow();
  const reviewerID = workflow?.reviewer_id || "";
  const revisionOwners = new Set();
  for (const task of workflow?.tasks || []) {
    if (!task.targeted_revision) continue;
    const evidence = state.evidence.get(`${workflow.project_name}/${task.task_id}`);
    if (evidence?.task?.assignee_id) revisionOwners.add(evidence.task.assignee_id);
  }
  ui.employeeGrid.replaceChildren(...employees.map((employee) => {
    const tasks = makerTasks.get(employee.id) || [];
    let responsibility = "Available";
    let kind = "";
    if (revisionOwners.has(employee.id)) {
      responsibility = "Revision";
      kind = "maker";
    } else if (employee.id === reviewerID) {
      responsibility = "Reviewer";
      kind = "reviewer";
    } else if (tasks.length) {
      responsibility = "Maker";
      kind = "maker";
    }
    const currentWork = employee.current_task || tasks[0] || (employee.id === reviewerID ? "成果物を独立レビュー" : "次の依頼を待機中");
    return node("article", { class: `employee-card ${kind}`.trim() },
      node("span", { class: "employee-avatar", "aria-hidden": "true" }),
      node("div", { class: "employee-name" },
        node("strong", {}, employee.name || employee.id),
        node("small", {}, `${employee.role || "役割未設定"} · ${employee.department || "所属未設定"}`),
        node("span", { class: "responsibility" }, responsibility),
        node("p", { class: "employee-task" }, currentWork),
      ),
    );
  }));
}

function latestWorkflow() {
  return [...(state.record?.turns || [])].reverse().find((turn) => turn.workflow)?.workflow || null;
}

function employeeLabel(id) {
  if (!id) return "未割当";
  const employee = (state.organization?.inventory?.employees || []).find((candidate) => candidate.id === id);
  return employee ? `${employee.name || employee.id} (${employee.id})` : id;
}

function renderCompanyFlow() {
  const workflow = latestWorkflow();
  const plan = state.record ? currentPlan() : null;
  const firstTask = workflow?.tasks?.[0];
  const plannedTask = plan?.plan?.proposed_tasks?.[0];
  const firstEvidence = firstTask ? state.evidence.get(`${workflow.project_name}/${firstTask.task_id}`) : null;
  const makerID = firstEvidence?.task?.assignee_id || plannedTask?.assignee_id || "";
  const revisionTask = workflow?.tasks?.find((task) => task.targeted_revision);
  const revisionEvidence = revisionTask ? state.evidence.get(`${workflow.project_name}/${revisionTask.task_id}`) : null;
  const flow = [
    {
      kind: "worker", label: "Maker", owner: employeeLabel(makerID),
      work: firstTask?.task_id || plannedTask?.title || "Taskを受け取り成果物を作る",
      state: firstTask ? "completed" : state.record ? "waiting" : "standby",
    },
    {
      kind: "reviewer", label: "Reviewer", owner: employeeLabel(workflow?.reviewer_id),
      work: firstTask ? `Review ${firstTask.verdict || "進行待ち"}` : "Makerとは別の視点で確認する",
      state: firstTask?.verdict ? "completed" : "waiting",
    },
    {
      kind: "revision", label: "Revision", owner: employeeLabel(revisionEvidence?.task?.assignee_id),
      work: revisionTask ? `${revisionTask.task_id}で指摘を反映` : "Request Changesのときだけ担当へ戻す",
      state: revisionTask ? "completed" : "standby",
    },
  ];
  if (workflow?.failure) {
    flow.push({ kind: "revision", label: "Recovery", owner: "人間の確認待ち", work: `${workflow.failure.code} / ${workflow.failure.stage}`, state: "blocked" });
  }
  ui.companyFlow.replaceChildren(...flow.map((step, index) =>
    node("article", { class: `flow-step ${step.kind}` },
      node("span", { class: "flow-index" }, String(index + 1)),
      node("div", { class: "flow-copy" }, node("strong", {}, `${step.label} · ${step.owner}`), node("small", {}, step.work)),
      node("span", { class: `flow-state ${step.state}` }, stateLabel(step.state)),
    ),
  ));
}

function detailBlock(title, rows) {
  return node("section", { class: "detail-block" }, node("h3", {}, title), node("ul", {}, ...rows.map((row) => node("li", {}, row))));
}

function openRequestDialog() {
  ui.requestForm.reset();
  ui.requestDialog.showModal();
  setTimeout(() => document.querySelector("#request-text").focus(), 80);
}

async function prepareNewRequest(event) {
  event.preventDefault();
  try {
    const data = new FormData(ui.requestForm);
    const request = data.get("request")?.toString().trim();
    if (!request) return toast("依頼内容を入力してください。");
    const input = { version: INTERACTION_VERSION, session_id: sessionID(), request, current_time: now() };
    ui.requestDialog.close();
    setBusy(true, "依頼内容を確認しています", "まだWorkspaceやProviderは変更しません。");
    const plan = await requestJSON("/v1/interaction-plans", { method: "POST", body: JSON.stringify(input) });
    setBusy(false);
    showApprovalSheet({
      title: "この依頼を開始しますか？",
      description: "承認すると依頼だけを保存します。AIによる進め方の作成は次の確認で行います。",
      facts: [["依頼", request], ["AIサービス", "WorkCairnが接続設定から選択"]],
      technicalFacts: [["Request digest", plan.session.request_digest]],
      approveLabel: "依頼を開始",
      onApprove: async () => {
        localStorage.setItem(STORAGE_SESSION, input.session_id);
        await executeNextCommand({ operation: "interaction.start" }, {
          session_id: input.session_id, request, request_digest: plan.session.request_digest,
          model: plan.session.model, current_time: input.current_time,
        }, "依頼を保存しています", "Sessionを作成しています。", commandID());
      },
    });
  } catch (error) {
    if (ui.requestDialog.open) ui.requestDialog.close();
    showError(error, "依頼内容を確認できませんでした");
  }
}

function closeDialog(event) {
  const dialog = event.currentTarget.closest("dialog");
  if (dialog?.open) dialog.close();
}

ui.pairingForm.addEventListener("submit", pair);
ui.requestForm.addEventListener("submit", prepareNewRequest);
document.querySelector("#new-request-button").addEventListener("click", openRequestDialog);
document.querySelector("#company-request-button").addEventListener("click", openRequestDialog);
document.querySelector("#refresh-button").addEventListener("click", () => refreshCurrent());
ui.settingsButton.addEventListener("click", openSettingsDialog);
document.querySelector("#provider-status-refresh").addEventListener("click", refreshProviderStatus);
ui.myActionsTab.addEventListener("click", () => setView("actions"));
ui.companyTab.addEventListener("click", () => setView("company"));
ui.detailsPanel.addEventListener("toggle", () => { if (ui.detailsPanel.open) loadTaskEvidenceDetails(); });
document.querySelectorAll("[data-close-dialog]").forEach((control) => control.addEventListener("click", closeDialog));

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

setInterval(() => {
  if (!state.busy && !ui.actionDialog.open && !ui.requestDialog.open && !ui.setupDialog.open && document.visibilityState === "visible" && state.record) {
    refreshCurrent(true);
  }
}, 5000);

initialize();
