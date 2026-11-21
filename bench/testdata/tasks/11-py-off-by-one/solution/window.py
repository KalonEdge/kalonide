def moving_sum(values, size):
    """Return the sums of every run of `size` consecutive values.

    moving_sum([1, 2, 3, 4], 2) == [3, 5, 7]
    """
    if size <= 0:
        raise ValueError("size must be positive")
    return [sum(values[i:i + size]) for i in range(len(values) - size + 1)]
