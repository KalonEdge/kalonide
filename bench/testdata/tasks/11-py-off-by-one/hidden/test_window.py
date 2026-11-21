import unittest
from window import moving_sum


class TestMovingSum(unittest.TestCase):
    def test_examples(self):
        self.assertEqual(moving_sum([1, 2, 3, 4], 2), [3, 5, 7])
        self.assertEqual(moving_sum([5], 1), [5])
        self.assertEqual(moving_sum([1, 2], 3), [])
        self.assertEqual(moving_sum([], 1), [])

    def test_bad_size(self):
        with self.assertRaises(ValueError):
            moving_sum([1], 0)


if __name__ == "__main__":
    unittest.main()
