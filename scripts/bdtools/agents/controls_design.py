# U2.2: the design note for the Agents card (#1350, #1381). Exec'd by
# selftest.py with Control and PLANT defined. The note is plain HTML, so each
# plant edits it and the guard tests in distribution/packaging must say which
# state or ratio went missing.

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    # Only for the type checker: at run time selftest.py execs this file
    # with both names already in scope.
    from bdtools.agents.selftest import Control

NOTE = "docs/design/1350-agents.html"
HTML_PLANT = "<!-- PLANTED VIOLATION (scripts/bdtools/agents/selftest.py) -->"

CONTROLS = [
    Control(
        "DESIGN a token state dropped from the Agents design note",
        ("U2.2",),
        [(NOTE, ',st:"revoked"}', ',st:"expired"}')],
        "./packaging/",
        ["TestTheAgentsDesignNoteDrawsEveryTokenState"],
        module="distribution",
    ),
    Control(
        "DESIGN the denied confirmation left out of the Agents design note",
        ("U2.2",),
        [(NOTE, "Denied. Nothing was run.", "Declined.")],
        "./packaging/",
        ["TestTheAgentsDesignNoteDrawsEveryConfirmationState"],
        module="distribution",
    ),
    Control(
        "DESIGN a contrast ratio not stated in the Agents design note",
        ("U2.2",),
        [(NOTE, "<td>4.71</td><td>5.08</td>", "<td>4.71</td><td></td>")],
        "./packaging/",
        ["TestEveryContrastRatioInTheAgentsDesignNoteIsStatedAndMeetsItsLine"],
        module="distribution",
    ),
    Control(
        "DESIGN a contrast ratio below its line in the Agents design note",
        ("U2.2",),
        [(NOTE, "<td>4.71</td><td>5.08</td>", "<td>4.21</td><td>5.08</td>")],
        "./packaging/",
        ["TestEveryContrastRatioInTheAgentsDesignNoteIsStatedAndMeetsItsLine"],
        module="distribution",
    ),
]
