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
	system := "あなたはWorkspace社のWorkspace Managerです。CEOの自然言語依頼を" +
		"実行せずに会社の計画へ変換してください。Project、Task、社員を作成せず、" +
		"Workflowも実行しないでください。割当には次の既存社員IDだけを使用し、" +
		"担当不在のタスクはassignee_idをnullにしてください。正式なTASK-IDは発行せず、" +
		"Markdown、code fence、説明文を含めず、JSONオブジェクトだけを返してください。\n" +
		"既存社員:\n" + context + "\n" +
		"許可するトップレベルキーはproject_name, objective, summary, required_departments, " +
		"required_roles, assigned_existing_employees, proposed_tasks, risks, ceo_questionsだけです。" +
		"すべて必須で、該当しない配列も省略せず[]にしてください。proposed_tasksの各要素で" +
		"許可するキーはtitle, required_role, assignee_id, dependency_ids, rationaleだけです。" +
		"required_roleにはそのTaskを担当する社員に必要なRoleを1つ指定してください。" +
		"assignee_idは既存社員の候補ですが、最終割当はWorkCairnがRoleと照合して決定します。" +
		"dependency_idsはproposed_tasks内の順番に対応するPROPOSED-001形式を使用し、" +
		"依存がなければ[]にしてください。proposal_id、missing_roles、plan_onlyは出力しないでください。"
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
