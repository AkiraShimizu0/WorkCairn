package ceoplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
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
	// The same CanonicalRoleTitles(employees) the Structured Output schema
	// constrains steps[].required_role to (ADR-0048) — computed here from
	// the identical employees input, so the Prompt's own instruction and
	// the schema's enum can never list a different vocabulary. When the
	// roster contributes no usable Role titles, this degrades to the
	// pre-existing generic instruction rather than failing BuildPrompt
	// itself — the actual fail-closed stop for that case is
	// ceoplan.IntentJSONSchema, called separately before any Provider call.
	requiredRoleInstruction := `required_roleには、そのstepを担当するために必要なRoleを1つ指定してください。write・research・analyze・implementでは必須、"review"の場合だけ省略可能です。空文字列は禁止です。`
	if allowedRoles := CanonicalRoleTitles(employees); len(allowedRoles) > 0 {
		requiredRoleInstruction += "次のいずれか1つを、表記のまま正確に使用してください: " + strings.Join(allowedRoles, ", ") + "。"
	} else {
		requiredRoleInstruction += "既存社員一覧のRole表記に合わせてください。"
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
		"stepsとceo_questionsは必須です。該当しない配列も省略せず、空配列[]として出力してください。",
		"summaryやdescriptionに「placeholder」「junk」「TBD」などの仮の値を出力しないでください。必ず実際の判断内容を記述してください。",
		"",
		"## top-level fields",
		"project_name, objective, summary, steps, ceo_questions の5つ以外のfieldは出力しないでください。stepsとceo_questionsは必須です。project_name・objective・summaryは省略可能です（省略した場合はWorkCairnが決定的に補います。出力する場合は空白のみの文字列にしないでください）。",
		"",
		"## stepsの各要素",
		"kind, description, required_role, parallel_with_previous 以外のfieldは禁止です。kindとdescriptionは常に必須です。",
		`kindは "write", "research", "analyze", "implement", "review" のいずれか1つの文字列にしてください。それ以外の値は使用しないでください。`,
		"descriptionには、そのstepで何を行うかを具体的に記述してください。空文字列や空白のみの値は禁止です。",
		requiredRoleInstruction,
		"stepsの順序は、実施すべき順番のまま出力してください。依存関係IDは出力しないでください（WorkCairnが順序から構築します）。",
		"担当する具体的な社員は出力しないでください（WorkCairnがrequired_roleと組織台帳から決定します）。",
		"",
		"## parallel_with_previous（同時並行できるかどうか）",
		"各stepについて、直前のstepと同時に着手できるかどうかをtrue/falseで示してください（review以外は必須）。",
		"直前のstepの結果が必要な場合、または最初のstepの場合はfalseにしてください。",
		"複数のstepが互いに無関係に同時進行できる場合（例: 市場調査・競合調査・顧客分析のように独立した調査）、2番目以降の独立stepにtrueを指定してください。",
		"その後、それらの結果をまとめる・統合するstepが続く場合は、そのstepをfalseにしてください（直前までの並行stepすべての結果を必要とするという意味になります）。",
		"依存関係の具体的な組み方（IDやグラフ構造）はWorkCairnがこのtrue/falseから決定します。あなた自身が依存関係IDを考える必要はありません。",
		"",
		"## 出力例（構造の見本です。この値をそのままコピーせず、実際の判断内容に差し替えてください。独立した複数stepを並行させる場合はparallel_with_previousをtrueにしてください）",
		`{"project_name":"サンプルプロジェクト","objective":"目的の要約","summary":"2つの調査記事を並行して作成し、まとめ記事へ統合する","steps":[{"kind":"write","description":"市場動向についての記事を作成する","required_role":"Content Writer","parallel_with_previous":false},{"kind":"write","description":"競合サービスについての記事を作成する","required_role":"Content Writer","parallel_with_previous":true},{"kind":"write","description":"2つの記事を統合したまとめ記事を作成する","required_role":"Content Writer","parallel_with_previous":false},{"kind":"review","description":"内容と文字数を確認する"}],"ceo_questions":[]}`,
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
