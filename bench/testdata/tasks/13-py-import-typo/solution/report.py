from helpers import format_name, format_money


def line(first, last, amount):
    return f"{format_name(first, last)}: {format_money(amount)}"


if __name__ == "__main__":
    print(line("Ada", "Lovelace", 1234.5))
