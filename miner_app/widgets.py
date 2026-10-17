"""Custom widgets for the miner UI."""
from PySide6.QtCore import Qt
from PySide6.QtWidgets import QFrame, QVBoxLayout, QLabel


class StatTile(QFrame):
    """A single stat display: small uppercase label + big value + optional sub."""

    def __init__(self, label: str, parent=None):
        super().__init__(parent)
        self.setProperty("role", "card")

        v = QVBoxLayout(self)
        v.setContentsMargins(16, 14, 16, 14)
        v.setSpacing(4)

        self.label = QLabel(label)
        self.label.setProperty("role", "stat-label")
        v.addWidget(self.label)

        self.value = QLabel("—")
        self.value.setProperty("role", "stat-value")
        v.addWidget(self.value)

        self.sub = QLabel("")
        self.sub.setProperty("role", "stat-sub")
        v.addWidget(self.sub)

    def set_value(self, value: str, sub: str = ""):
        self.value.setText(value)
        self.sub.setText(sub)
