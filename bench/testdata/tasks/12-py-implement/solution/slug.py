import re


def slugify(text):
    """Turn text into a URL slug.

    Lowercase the text, replace every run of characters that are not ASCII
    letters or digits with a single hyphen, and strip hyphens from both ends.

    slugify("Hello, World!") == "hello-world"
    """
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")
