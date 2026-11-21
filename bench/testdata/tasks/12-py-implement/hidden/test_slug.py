import unittest
from slug import slugify


class TestSlugify(unittest.TestCase):
    def test_cases(self):
        cases = {
            "Hello, World!": "hello-world",
            "  Go 1.22 release  ": "go-1-22-release",
            "already-a-slug": "already-a-slug",
            "Ünïcode & Friends": "n-code-friends",
            "---": "",
            "": "",
            "MiXeD   Case__words": "mixed-case-words",
        }
        for text, want in cases.items():
            self.assertEqual(slugify(text), want, text)


if __name__ == "__main__":
    unittest.main()
