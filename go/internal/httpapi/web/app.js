const INTERACTION_VERSION = "workspace-interaction.v1";
const COMMAND_VERSION = "workspace-command.v1";
const STORAGE_SESSION = "workspace-os.active-session";
const STORAGE_MODEL = "workspace-os.preferred-model";
const STORAGE_PENDING = "workspace-os.pending-command";

const ui = {
  pairingView: document.querySelector("#pairing-view"),
  pairingForm: document.querySelector("#pairing-form"),
  workspaceView: document.querySelector("#workspace-view"),
  status: document.querySelector("#connection-status"),
  activeCard: document.querySelector("#active-card"),
  detailsPanel: document.querySelector("#details-panel"),
  details: document.querySelector("#details-content"),
  sessionList: document.querySelector("#session-list"),
  requestDialog: document.querySelector("#request-dialog"),
  requestForm: document.querySelector("#request-form"),
  actionDialog: document.querySelector("#action-dialog"),
  actionForm: document.querySelector("#action-form"),
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
function commandID() { return `CMD-${crypto.randomUUID().toUpperCase()}`; }
function sessionID() { return `SESSION-${Date.now()}-${crypto.randomUUID().slice(0, 8).toUpperCase()}`; }
function projectID() { return `PROJECT-${Date.now()}-${crypto.randomUUID().slice(0, 6).toUpperCase()}`; }

function stateLabel(value) {
  const labels = {
    plan_generation_approval_required: "Plan作成待ち",
    clarification_required: "回答待ち",
    plan_approval_required: "Plan承認待ち",
    ready_to_execute: "実行承認待ち",
    workflow_attention_required: "確認が必要",
    completed: "仕事完了",
    action_completed: "公開完了",
    action_attention_required: "公開確認が必要",
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

let toastTimer;
function toast(message) {
  clearTimeout(toastTimer);
  ui.toast.textContent = message;
  ui.toast.hidden = false;
  toastTimer = setTimeout(() => { ui.toast.hidden = true; }, 4200);
}

function showError(error, title = "処理を完了できませんでした") {
  setBusy(false);
  const detail = error instanceof APIError ? error.detail : null;
  const code = detail?.code || error.message || "UNKNOWN_ERROR";
  const stage = detail?.stage;
  ui.activeCard.className = "action-card attention";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "確認が必要です"),
    node("h2", {}, title),
    node("p", { class: "lead" }, "成立済みの記録を推測で変更せず、現在の状態を確認してください。"),
    node("div", { class: "error-box" },
      node("strong", {}, code),
      stage ? node("div", {}, `stage: ${stage}`) : null,
    ),
    node("div", { class: "button-row" },
      button("状態を再確認", "primary", () => refreshCurrent()),
      button("依頼一覧へ", "quiet", () => selectSession(null)),
    ),
  );
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
  setConnected(true);
  await loadSessions();
  const stored = localStorage.getItem(STORAGE_SESSION);
  const candidate = state.sessions.find((record) => record.session_id === stored) ||
    state.sessions.find((record) => !["completed", "action_completed"].includes(record.state)) || state.sessions[0];
  await selectSession(candidate?.session_id || null);
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
    localStorage.removeItem(STORAGE_SESSION);
    renderEmpty();
    renderDetails();
    return;
  }
  localStorage.setItem(STORAGE_SESSION, id);
  await refreshCurrent();
}

async function refreshCurrent(silent = false) {
  const id = localStorage.getItem(STORAGE_SESSION);
  if (!id) {
    renderEmpty();
    return;
  }
  try {
    const [record, next] = await Promise.all([
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}`),
      requestJSON(`/v1/interactions/${encodeURIComponent(id)}/next`),
    ]);
    state.record = record;
    state.next = next;
    setConnected(true);
    renderNext();
    renderDetails();
    await loadSessions();
  } catch (error) {
    setConnected(false);
    if (!silent) showError(error, "依頼の状態を取得できませんでした");
  }
}

function renderEmpty() {
  ui.activeCard.className = "action-card complete";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "準備できています"),
    node("h2", {}, "新しい仕事を依頼してください"),
    node("p", { class: "lead" }, "やりたいことを自然な言葉で入力すると、必要な質問と承認だけを順に表示します。"),
    node("div", { class: "button-row" }, button("仕事を依頼する", "primary", openRequestDialog)),
  );
}

function renderSessions() {
  if (!state.sessions.length) {
    ui.sessionList.replaceChildren(node("p", { class: "empty" }, "まだ依頼はありません。"));
    return;
  }
  ui.sessionList.replaceChildren(...state.sessions.map((record) =>
    node("button", { class: "session-item", type: "button", onclick: () => selectSession(record.session_id) },
      node("span", {},
        node("strong", {}, record.request),
        node("small", {}, new Date(record.created_at).toLocaleString("ja-JP")),
      ),
      node("span", { class: "state-chip" }, stateLabel(record.state)),
    ),
  ));
}

function renderNext() {
  const next = state.next;
  if (!next) return renderEmpty();
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

function renderPlanGeneration(next) {
  const hasAnswers = state.record.turns.some((turn) => turn.kind === "clarification_answered");
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, hasAnswers ? "回答を反映" : "最初の確認"),
    node("h2", {}, hasAnswers ? "回答をもとにPlanを作り直します" : "仕事のPlanを作成します"),
    node("p", { class: "lead" }, "AIはまだWorkspaceを変更しません。依頼内容を整理し、必要な質問と実行Planを作成します。"),
    approvalFacts([
      ["依頼", state.record.request],
      ["モデル", state.record.model],
      ["Session", state.record.session_id],
    ]),
    node("div", { class: "button-row" },
      button("Plan作成を承認", "primary", () => executeNextCommand(next, {
        session_id: next.session_id,
        expected_version: next.expected_version,
        current_time: now(),
      }, "Planを作成しています", "質問または実行Planができるまでお待ちください。")),
      button("今は承認しない", "quiet", () => toast("変更せず、承認待ちのまま保存されています。")),
    ),
  );
}

function renderQuestions(next) {
  const form = node("form", { class: "question-list" });
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
    executeNextCommand(next, { session_id: next.session_id, expected_version: next.expected_version, answers, current_time: now() }, "回答を保存しています", "保存後に次のPlan作成確認へ進みます。");
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

function renderPlanApproval(next) {
  const current = currentPlan();
  if (!current) return showError(new Error("Plan evidence is missing"));
  const identifier = localStorage.getItem(`workspace-os.project.${state.record.session_id}`) || projectID();
  localStorage.setItem(`workspace-os.project.${state.record.session_id}`, identifier);
  const projectInput = node("input", { id: "project-id", value: identifier, required: true, autocomplete: "off", spellcheck: "false" });
  const tasks = node("ul", {}, ...current.plan.proposed_tasks.map((task) => node("li", {}, `${task.title}${task.assignee_id ? ` — ${task.assignee_id}` : ""}`)));
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "Project作成の承認"),
    node("h2", {}, current.plan.project_name),
    node("p", { class: "lead" }, current.plan.summary),
    node("div", { class: "approval-box" },
      node("strong", {}, "実行する内容"),
      tasks,
      node("label", { for: "project-id" }, "Project ID"),
      projectInput,
      approvalFacts([["Plan digest", shortDigest(current.digest)], ["Task数", String(current.plan.proposed_tasks.length)]]),
    ),
    node("div", { class: "button-row" },
      button("このPlanを承認して作成", "primary", () => {
        const value = projectInput.value.trim();
        if (!value) return toast("Project IDを入力してください。");
        localStorage.setItem(`workspace-os.project.${state.record.session_id}`, value);
        executeNextCommand(next, {
          session_id: next.session_id, expected_version: next.expected_version,
          project_id: value, plan_digest: current.digest, current_time: now(),
        }, "Projectを作成しています", "ProjectとTaskを安全な順序で保存しています。");
      }),
      button("承認しない", "quiet", () => toast("Workspaceは変更されていません。")),
    ),
  );
}

async function loadOrganization() {
  if (!state.organization) state.organization = await requestJSON("/v1/organization");
  return state.organization;
}

async function renderWorkflowApproval(next) {
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "実行準備"),
    node("h2", {}, "担当AIに仕事を開始させます"),
    node("p", { class: "lead" }, "各Taskを実行し、Reviewで修正が必要ならRevisionと再Reviewまで進めます。開始前にReviewerと上限を確認します。"),
    node("p", { class: "empty" }, "Reviewerを読み込んでいます…"),
  );
  try {
    const inspection = await loadOrganization();
    if (state.next?.kind !== "approve_workflow") return;
    const employees = inspection.inventory?.employees || [];
    const form = node("form", { class: "stack-form" });
    let reviewerControl;
    if (employees.length) {
      reviewerControl = node("select", { id: "reviewer-id", name: "reviewer", required: true },
        node("option", { value: "" }, "Reviewerを選択"),
        ...employees.map((employee) => node("option", { value: employee.id }, `${employee.name || employee.id} — ${employee.role || "役割未設定"}`)),
      );
    } else {
      reviewerControl = node("input", { id: "reviewer-id", name: "reviewer", required: true, placeholder: "例：QA-001" });
    }
    const maxTasks = node("input", { id: "max-tasks", name: "max_tasks", type: "number", min: "1", max: "100", value: "20", inputmode: "numeric", required: true });
    form.append(
      node("label", { for: "reviewer-id" }, "Reviewer"), reviewerControl,
      node("label", { for: "max-tasks" }, "今回実行するTask上限"), maxTasks,
      node("div", { class: "button-row" },
        button("実行内容を確認", "primary", null, "submit"),
        button("今は実行しない", "quiet", () => toast("Taskは開始されていません。")),
      ),
    );
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const reviewerID = reviewerControl.value.trim();
      const limit = Number(maxTasks.value);
      if (!reviewerID || !Number.isInteger(limit) || limit < 1 || limit > 100) return toast("Reviewerと1〜100のTask上限を確認してください。");
      await prepareWorkflowApproval(next, reviewerID, limit);
    });
    ui.activeCard.replaceChildren(
      node("span", { class: "step-label" }, "実行準備"),
      node("h2", {}, "担当AIに仕事を開始させます"),
      node("p", { class: "lead" }, "各Taskを実行し、Reviewで修正が必要ならRevisionと再Reviewまで進めます。"),
      form,
    );
  } catch (error) {
    showError(error, "Reviewerを取得できませんでした");
  }
}

async function prepareWorkflowApproval(next, reviewerID, maxTasks) {
  const currentTime = now();
  setBusy(true, "実行内容を確認しています", "TaskとReviewerの現在状態をread-onlyで検証しています。");
  try {
    const plan = await requestJSON("/v1/interaction-workflow-plans", {
      method: "POST",
      body: JSON.stringify({ version: INTERACTION_VERSION, session_id: next.session_id, expected_version: next.expected_version, reviewer_id: reviewerID, current_time: currentTime, max_tasks: maxTasks }),
    });
    setBusy(false);
    showApprovalSheet({
      title: "Workflowを実行しますか？",
      description: "承認後はTask実行、Review、必要なRevisionを上限まで自律的に進めます。",
      facts: [
        ["Project", `${plan.project_name} (${plan.project_id})`],
        ["Reviewer", `${plan.reviewer_name} (${plan.reviewer_id})`],
        ["次のTask", plan.next?.task_id || "readinessに従う"],
        ["Task上限", String(plan.max_tasks)],
        ["Plan digest", plan.workflow_plan_digest],
      ],
      approveLabel: "承認して実行",
      onApprove: () => executeNextCommand(next, {
        session_id: next.session_id, expected_version: next.expected_version,
        reviewer_id: reviewerID, current_time: currentTime, max_tasks: maxTasks,
        workflow_plan_digest: plan.workflow_plan_digest,
        approval_reference: `mobile-ui:${next.session_id}:v${next.expected_version}`,
      }, "Workflowを実行しています", "Task、Review、必要なRevisionを順番に進めています。"),
    });
  } catch (error) {
    showError(error, "Workflow planを確認できませんでした");
  }
}

function renderCompletion(next) {
  ui.activeCard.className = "action-card complete";
  ui.activeCard.replaceChildren(
    node("span", { class: "step-label" }, "仕事が完了しました"),
    node("h2", {}, "すべてのTaskとReviewが完了しています"),
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
  const outerCommandID = commandID();
  const currentTime = now();
  setBusy(true, "公開内容を確認しています", "成果物digestと公開対象をread-onlyで検証しています。");
  try {
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

function showApprovalSheet({ title, description, facts, approveLabel, onApprove, approveKind = "primary", hideCancel = false }) {
  const form = ui.actionForm;
  form.replaceChildren(
    node("div", { class: "sheet-handle" }),
    node("div", { class: "sheet-heading" },
      node("div", {}, node("p", { class: "eyebrow" }, "CONFIRMATION"), node("h2", {}, title)),
      node("button", { class: "icon-button", type: "button", "aria-label": "閉じる", onclick: closeActionDialog }, "×"),
    ),
    node("p", { class: "supporting" }, description),
    approvalFacts(facts),
    node("div", { class: "sheet-actions" },
      hideCancel ? null : button("承認しない", "quiet", closeActionDialog),
      button(approveLabel, approveKind, null, "submit"),
    ),
  );
  form.onsubmit = (event) => {
    event.preventDefault();
    closeActionDialog();
    onApprove();
  };
  ui.actionDialog.showModal();
}

function approvalFacts(facts) {
  return node("div", { class: "approval-box" }, node("dl", {}, ...facts.map(([term, value]) =>
    node("div", {}, node("dt", {}, term), node("dd", { class: String(value).startsWith("sha256:") ? "digest" : "" }, value || "—")),
  )));
}

async function executeNextCommand(next, payload, busyTitle, busyMessage, fixedCommandID = null) {
  const command = {
    version: COMMAND_VERSION,
    command_id: fixedCommandID || commandID(),
    operation: next.operation,
    approved: true,
    payload,
  };
  sessionStorage.setItem(STORAGE_PENDING, JSON.stringify(command));
  setBusy(true, busyTitle, busyMessage);
  try {
    await requestJSON("/v1/commands", { method: "POST", body: JSON.stringify(command) });
    sessionStorage.removeItem(STORAGE_PENDING);
    await refreshCurrent();
    setBusy(false);
  } catch (error) {
    if (error.status !== 0) sessionStorage.removeItem(STORAGE_PENDING);
    showError(error);
  }
}

function renderDetails() {
  const record = state.record;
  if (!record) {
    ui.detailsPanel.hidden = true;
    ui.details.replaceChildren();
    return;
  }
  ui.detailsPanel.hidden = false;
  const blocks = [
    detailBlock("依頼", [record.request, `Session: ${record.session_id}`, `Version: ${record.version} / ${stateLabel(record.state)}`]),
  ];
  const current = currentPlan();
  if (current) {
    blocks.push(detailBlock("Plan", [
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
  } catch (error) {
    toast(`成果物の詳細を取得できませんでした: ${error.message}`);
  }
}

function detailBlock(title, rows) {
  return node("section", { class: "detail-block" }, node("h3", {}, title), node("ul", {}, ...rows.map((row) => node("li", {}, row))));
}

function openRequestDialog() {
  ui.requestForm.reset();
  document.querySelector("#model-name").value = localStorage.getItem(STORAGE_MODEL) || "Claude Sonnet 5";
  ui.requestDialog.showModal();
  setTimeout(() => document.querySelector("#request-text").focus(), 80);
}

async function prepareNewRequest(event) {
  event.preventDefault();
  const data = new FormData(ui.requestForm);
  const request = data.get("request")?.toString().trim();
  const model = data.get("model")?.toString().trim();
  if (!request || !model) return;
  localStorage.setItem(STORAGE_MODEL, model);
  const input = { version: INTERACTION_VERSION, session_id: sessionID(), request, model, current_time: now() };
  ui.requestDialog.close();
  setBusy(true, "依頼内容を確認しています", "まだWorkspaceやProviderは変更しません。");
  try {
    const plan = await requestJSON("/v1/interaction-plans", { method: "POST", body: JSON.stringify(input) });
    setBusy(false);
    showApprovalSheet({
      title: "この依頼を開始しますか？",
      description: "承認するとInteraction Sessionだけを作成します。AIによるPlan作成は次の確認で行います。",
      facts: [["依頼", request], ["モデル", model], ["Request digest", plan.session.request_digest]],
      approveLabel: "依頼を開始",
      onApprove: async () => {
        localStorage.setItem(STORAGE_SESSION, input.session_id);
        await executeNextCommand({ operation: "interaction.start" }, {
          session_id: input.session_id, request, request_digest: plan.session.request_digest,
          model, current_time: input.current_time,
        }, "依頼を保存しています", "Sessionを作成しています。", commandID());
      },
    });
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
document.querySelector("#new-request-button").addEventListener("click", openRequestDialog);
document.querySelector("#refresh-button").addEventListener("click", () => refreshCurrent());
ui.detailsPanel.addEventListener("toggle", () => { if (ui.detailsPanel.open) loadTaskEvidenceDetails(); });
document.querySelectorAll("[data-close-dialog]").forEach((control) => control.addEventListener("click", closeDialog));

async function initialize() {
  try {
    const access = await requestJSON("/v1/local-access/status");
    if (!access.authenticated) {
      ui.pairingView.hidden = false;
      ui.workspaceView.hidden = true;
      setConnected(false);
      return;
    }
    await startWorkspace();
    const pending = sessionStorage.getItem(STORAGE_PENDING);
    if (pending) toast("前回のCommand結果が未確認です。Session状態を確認してから必要なら同じCommand IDで明示再送してください。");
  } catch (error) {
    setConnected(false);
    ui.pairingView.hidden = false;
    toast(error.message);
  }
}

setInterval(() => {
  if (!state.busy && !ui.actionDialog.open && !ui.requestDialog.open && !ui.detailsPanel.open && document.visibilityState === "visible" && state.record) {
    refreshCurrent(true);
  }
}, 5000);

initialize();
