from helpers import format_nmae, format_money


def line(first, last, amount):
    return f"{format_nmae(first, last)}: {format_money(amount)}"


if __name__ == "__main__":
    print(line("Ada", "Lovelace", 1234.5))
