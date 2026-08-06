import unittest

from workspace_ai.review_result import parse_review_output


class ReviewResultTest(unittest.TestCase):
    def test_extracts_and_validates_structured_result(self):
        human, result = parse_review_output(
            "## レビュー\n\n日付を修正してください。\n\n"
            "REVIEW_RESULT_JSON_START\n"
            '{"verdict":"Request Changes","issues":[{'
            '"category":"date","severity":"high",'
            '"description":"日付が矛盾しています。",'
            '"suggested_action":"executed_atに合わせてください。"}]}\n'
            "REVIEW_RESULT_JSON_END"
        )

        self.assertEqual(human, "## レビュー\n\n日付を修正してください。")
        self.assertEqual(result["verdict"], "Request Changes")
        self.assertEqual(result["issues"][0]["category"], "date")

    def test_rejects_invalid_json_and_empty_request_changes(self):
        with self.assertRaisesRegex(ValueError, "JSONが不正"):
            parse_review_output(
                "レビュー\nREVIEW_RESULT_JSON_START\n{invalid}\n"
                "REVIEW_RESULT_JSON_END"
            )
        with self.assertRaisesRegex(ValueError, "1件以上"):
            parse_review_output(
                "レビュー\nREVIEW_RESULT_JSON_START\n"
                '{"verdict":"Request Changes","issues":[]}\n'
                "REVIEW_RESULT_JSON_END"
            )


if __name__ == "__main__":
    unittest.main()
