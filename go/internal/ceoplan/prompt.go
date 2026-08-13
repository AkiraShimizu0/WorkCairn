package ceoplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

func BuildPrompt(request string, employees []organization.Identity) (worker.Prompt, error) {
	request = strings.TrimSpace(request)
	if request == "" {
		return worker.Prompt{}, ErrInvalidRequest
	}
	if _, _, err := employeeIndex(employees); err != nil {
		return worker.Prompt{}, err
	}
	context, err := renderEmployeeContext(employees)
	if err != nil {
		return worker.Prompt{}, err
	}
	system := strings.Join([]string{
		"あなたはWorkspace社のWorkspace Managerです。CEOの自然言語依頼を実行せずに会社の計画へ変換してください。",
		"Project、Task、社員を作成せず、Workflowも実行しないでください。",
		"",
		"## 既存社員",
		context,
		"",
		"## 必須出力ルール（例外なし）",
		"JSONオブジェクトだけを返してください。前後にMarkdown、code fence（```）、説明文を一切含めないでください。",
		"下記のtop-level fieldとproposed_tasks fieldの一覧にない、それ以外のfieldを一切追加しないでください。",
		"top-level fieldはすべて必須です。該当しない配列も省略せず、空配列[]として出力してください。",
		"正式なTASK-ID、proposal_id、missing_roles、plan_onlyは出力しないでください（WorkCairnが後で決定・付与します）。",
		"",
		"## top-level fields",
		"project_name, objective, summary, required_departments, required_roles, assigned_existing_employees, proposed_tasks, risks, ceo_questions の9つだけを、この順序で出力してください。",
		"",
		"## proposed_tasksの各要素",
		"title, required_role, assignee_id, dependency_ids, rationale の5つだけを持つオブジェクトにしてください。",
		"",
		"## assignment rules",
		"assigned_existing_employeesとproposed_tasksのassignee_idには、上記「既存社員」に列挙されたIDだけを使用してください。",
		"required_roleには、そのTaskを担当する社員に必要なRoleを1つ指定してください。",
		"assignee_idは既存社員IDの候補として指定できますが、最終的な割当はWorkCairnがRoleと照合して決定します。担当が明確でない場合はnullにしてください。",
		"",
		"## dependency rules",
		"dependency_idsは、proposed_tasks内の順番に対応するPROPOSED-001形式のID（1件目はPROPOSED-001、2件目はPROPOSED-002、…）を使用してください。",
		"依存するTaskがない場合は空配列[]にしてください。",
		"",
		"## 出力例（構造の見本です。この値をそのままコピーせず、実際の判断内容に差し替えてください）",
		`{"project_name":"サンプルプロジェクト","objective":"目的の要約","summary":"補足説明","required_departments":["コンテンツ部"],"required_roles":["Content Writer"],"assigned_existing_employees":[],"proposed_tasks":[{"title":"最初のタスク","required_role":"Content Writer","assignee_id":null,"dependency_ids":[],"rationale":"理由"}],"risks":[],"ceo_questions":[]}`,
	}, "\n")
	return worker.Prompt{System: system, User: request}, nil
}

func renderEmployeeContext(employees []organization.Identity) (string, error) {
	items := make([]string, 0, len(employees))
	for _, employee := range employees {
		department, err := jsonString(employee.Department)
		if err != nil {
			return "", err
		}
		id, err := jsonString(employee.ID)
		if err != nil {
			return "", err
		}
		role, err := jsonString(employee.Role)
		if err != nil {
			return "", err
		}
		items = append(items, fmt.Sprintf(`{"department": %s, "id": %s, "role": %s}`, department, id, role))
	}
	return "[" + strings.Join(items, ", ") + "]", nil
}

func jsonString(value string) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}
