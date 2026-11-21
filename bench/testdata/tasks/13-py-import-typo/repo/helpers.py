def format_name(first, last):
    """Return 'Last, First'."""
    return f"{last}, {first}"


def format_money(amount):
    """Return an amount like '$1,234.50'."""
    return f"${amount:,.2f}"
