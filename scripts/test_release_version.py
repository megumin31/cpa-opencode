"""Offline tag selection and workflow routing contract."""
import unittest
from pathlib import Path
from release_version import build_version, parse_tag

class VersionTests(unittest.TestCase):
    def test_branch_and_pr_are_development_builds(self):
        for ref, event in [('refs/heads/main','push'), ('refs/pull/1/merge','pull_request')]:
            self.assertEqual(build_version(ref,event),'0.0.0')
    def test_stable_and_prerelease_tags(self):
        self.assertEqual(parse_tag('v1.2.3'), ('1.2.3', False))
        self.assertEqual(parse_tag('v1.2.3-rc.1'), ('1.2.3', True))
        self.assertEqual(build_version('refs/tags/v1.2.3-rc.1', 'push'), '1.2.3')
    def test_invalid_tags_fail_closed(self):
        for tag in ['v1','v1.2','v01.2.3','v1.2.3/escape','v1.2.3\n','v1.2.3-','v1.2.3;echo bad','v1.2.3+meta']:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                build_version('refs/tags/'+tag, 'push')
    def test_non_push_tag_is_rejected(self):
        with self.assertRaises(ValueError):
            build_version('refs/tags/v1.2.3','release')
    def test_workflow_has_one_publication_path(self):
        workflow = (Path(__file__).parents[1]/'.github/workflows/build-plugin.yml').read_text()
        self.assertIn('branches: ["**"]', workflow)
        self.assertIn('tags: ["v*"]', workflow)
        self.assertIn('  pull_request:', workflow)
        self.assertNotIn('  release:', workflow)
        self.assertNotIn('workflow_dispatch:', workflow)
        self.assertEqual(workflow.count('contents: write'), 1)
        self.assertIn('needs: build', workflow)
        self.assertIn("github.event_name == 'push' && startsWith(github.ref, 'refs/tags/')", workflow)
