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
		"あなたはWorkspace社のWorkspace Managerです。CEOの自然言語依頼を実行せずに、意味理解と作業方針の提案だけを行ってください。",
		"Project、Task、社員を作成せず、Workflowも実行しないでください。",
		"誰を割り当てるか、正式なID、依存関係の具体的な組み方はWorkCairnがGoで決定します。あなたはその判断に必要ない情報を出力しないでください。",
		"",
		"## 参考: 既存社員のRole一覧（required_roleの語彙合わせにのみ使用してください。IDは出力に含めません）",
		context,
		"",
		"## 必須出力ルール（例外なし）",
		"JSONオブジェクトだけを返してください。前後にMarkdown、code fence（```）、説明文を一切含めないでください。",
		"下記のtop-level fieldとsteps fieldの一覧にない、それ以外のfieldを一切追加しないでください。",
		"top-level fieldはすべて必須です。該当しない配列も省略せず、空配列[]として出力してください。",
		"",
		"## top-level fields",
		"project_name, objective, summary, steps, ceo_questions の5つだけを、この順序で出力してください。",
		"",
		"## stepsの各要素",
		"kind, description, required_role 以外のfieldは禁止です。kindとdescriptionは常に必須です。",
		`kindは "write", "research", "analyze", "implement", "review" のいずれか1つの文字列にしてください。それ以外の値は使用しないでください。`,
		"descriptionには、そのstepで何を行うかを具体的に記述してください。",
		`required_roleには、そのstepを担当するために必要なRoleを1つ指定してください。write・research・analyze・implementでは必須、"review"の場合だけ省略可能です。空文字列は禁止です。既存社員一覧のRole表記に合わせてください。`,
		"stepsの順序は、実施すべき順番のまま出力してください。依存関係IDは出力しないでください（WorkCairnが順序から構築します）。",
		"担当する具体的な社員は出力しないでください（WorkCairnがrequired_roleと組織台帳から決定します）。",
		"",
		"## 出力例（構造の見本です。この値をそのままコピーせず、実際の判断内容に差し替えてください）",
		`{"project_name":"サンプルプロジェクト","objective":"目的の要約","summary":"補足説明","steps":[{"kind":"write","description":"100文字程度の紹介文を作成する","required_role":"Content Writer"},{"kind":"review","description":"内容と文字数を確認する"}],"ceo_questions":[]}`,
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
