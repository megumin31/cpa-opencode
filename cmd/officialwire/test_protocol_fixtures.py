"""Offline regression checks for the synthetic official-binary probe fixtures."""
import json
import unittest

from protocol_fixtures import CALL_ID, protocol_spec, response_frames, tool_result


class ProtocolFixturesTest(unittest.TestCase):
    def test_complete_json_sse_and_tool_arguments(self):
        for protocol in ('chat', 'responses', 'anthropic', 'gemini'):
            for arguments in (None, {'path': '/tmp/synthetic-only'}, {'invalid_field': 'fake'}):
                with self.subTest(protocol=protocol, arguments=arguments):
                    raw = response_frames(protocol, arguments).decode()
                    self.assertTrue(raw.endswith('\n\n'))
                    frames = [json.loads(line[6:]) for line in raw.splitlines()
                              if line.startswith('data: ') and line != 'data: [DONE]']
                    self.assertTrue(frames)
                    self.assertIn('10', raw)
                    self.assertIn('OK' if arguments is None else 'read', raw)
                    if arguments is not None:
                        self.assertIn('path' if 'path' in arguments else 'invalid_field', raw)

    def test_matching_tool_results(self):
        samples = {
            'chat': {'messages': [{'role': 'tool', 'tool_call_id': CALL_ID, 'content': 'fixture'}]},
            'responses': {'input': [{'type': 'function_call_output', 'call_id': CALL_ID, 'output': 'fixture'}]},
            'anthropic': {'messages': [{'role': 'user', 'content': [{'type': 'tool_result', 'tool_use_id': CALL_ID, 'content': 'fixture'}]}]},
            'gemini': {'contents': [{'parts': [{'functionResponse': {'name': 'read', 'id': CALL_ID, 'response': 'fixture'}}]}]},
        }
        for protocol, sample in samples.items():
            with self.subTest(protocol=protocol):
                self.assertEqual(tool_result(protocol, sample), 'fixture')
                with self.assertRaises(ValueError):
                    tool_result(protocol, {})
                mismatched = json.loads(json.dumps(sample).replace(CALL_ID, 'different_call'))
                with self.assertRaises(ValueError):
                    tool_result(protocol, mismatched)

    def test_unknown_protocol_and_invalid_arguments_rejected(self):
        with self.assertRaises(ValueError):
            protocol_spec('unknown')
        with self.assertRaises(TypeError):
            response_frames('responses', 'not-an-object')


if __name__ == '__main__':
    unittest.main()
