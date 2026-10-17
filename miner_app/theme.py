"""Qt stylesheet matching the EntropyChain wallet/explorer aesthetic."""

# Color palette — same as wallet.html
BG          = "#07090d"
BG_2        = "#0c1118"
PANEL       = "#0f161f"
PANEL_2     = "#131b27"
BORDER      = "#1a2433"
BORDER_HI   = "#2a3a52"
TEXT        = "#d2dae6"
TEXT_BRIGHT = "#ffffff"
MUTED       = "#6c7990"
DIM         = "#404a5e"
ACCENT      = "#00d4aa"
ACCENT_2    = "#ff6b9d"
WARN        = "#ffb454"
ERR         = "#ff5959"

STYLESHEET = f"""
QMainWindow, QDialog {{
    background-color: {BG};
    color: {TEXT};
}}

QWidget {{
    background-color: {BG};
    color: {TEXT};
    font-family: "Segoe UI", "SF Pro Display", system-ui, sans-serif;
    font-size: 13px;
}}

QLabel {{
    background: transparent;
    color: {TEXT};
}}

QLabel[role="header"] {{
    font-size: 22px;
    font-weight: 300;
    color: {TEXT_BRIGHT};
}}

QLabel[role="brand"] {{
    font-family: "Consolas", "Menlo", monospace;
    font-weight: 500;
    font-size: 14px;
    letter-spacing: 1px;
}}

QLabel[role="muted"] {{
    color: {MUTED};
    font-size: 11px;
    letter-spacing: 1px;
}}

QLabel[role="stat-label"] {{
    color: {MUTED};
    font-size: 10px;
    letter-spacing: 2px;
    text-transform: uppercase;
}}

QLabel[role="stat-value"] {{
    color: {TEXT_BRIGHT};
    font-size: 22px;
    font-weight: 500;
    font-family: "Consolas", "Menlo", monospace;
}}

QLabel[role="stat-sub"] {{
    color: {DIM};
    font-size: 11px;
}}

QLabel[role="address"] {{
    color: {ACCENT_2};
    font-family: "Consolas", "Menlo", monospace;
    font-size: 11px;
}}

QLabel[role="status-good"] {{
    color: {ACCENT};
    font-size: 11px;
}}

QLabel[role="status-warn"] {{
    color: {WARN};
    font-size: 11px;
}}

QLabel[role="status-err"] {{
    color: {ERR};
    font-size: 11px;
}}

QFrame[role="card"] {{
    background-color: {PANEL};
    border: 1px solid {BORDER};
    border-radius: 8px;
}}

QFrame[role="separator"] {{
    background-color: {BORDER};
    max-height: 1px;
    border: none;
}}

QPushButton {{
    background-color: {ACCENT};
    color: {BG};
    border: none;
    border-radius: 4px;
    padding: 10px 18px;
    font-size: 13px;
    font-weight: 500;
}}

QPushButton:hover {{
    background-color: {ACCENT_2};
}}

QPushButton:disabled {{
    background-color: {BORDER};
    color: {MUTED};
}}

QPushButton[role="secondary"] {{
    background-color: transparent;
    border: 1px solid {BORDER_HI};
    color: {TEXT};
}}

QPushButton[role="secondary"]:hover {{
    border-color: {ACCENT};
    color: {ACCENT};
}}

QPushButton[role="big"] {{
    background-color: {ACCENT};
    color: {BG};
    font-size: 15px;
    font-weight: 500;
    padding: 14px 24px;
    border-radius: 6px;
}}

QPushButton[role="big"]:hover {{
    background-color: {ACCENT_2};
}}

QPushButton[role="stop"] {{
    background-color: {ERR};
    color: {BG};
    font-size: 15px;
    font-weight: 500;
    padding: 14px 24px;
    border-radius: 6px;
}}

QPushButton[role="stop"]:hover {{
    background-color: #cc4444;
}}

QPushButton[role="link"] {{
    background-color: transparent;
    color: {MUTED};
    border: 1px solid {BORDER};
    padding: 4px 10px;
    font-size: 11px;
}}

QPushButton[role="link"]:hover {{
    color: {ACCENT};
    border-color: {ACCENT};
}}

QLineEdit {{
    background-color: {BG_2};
    color: {TEXT};
    border: 1px solid {BORDER};
    border-radius: 4px;
    padding: 8px 10px;
    font-family: "Consolas", "Menlo", monospace;
    font-size: 12px;
    min-height: 20px;
}}

QSpinBox {{
    background-color: {BG_2};
    color: {TEXT};
    border: 1px solid {BORDER};
    border-radius: 4px;
    padding: 8px 10px;
    font-family: "Consolas", "Menlo", monospace;
    font-size: 12px;
    min-height: 20px;
}}

QSpinBox:focus {{
    border: 1px solid {ACCENT};
}}

QLineEdit:focus {{
    border: 1px solid {ACCENT};
}}

QPlainTextEdit {{
    background-color: {BG};
    color: {MUTED};
    border: 1px solid {BORDER};
    border-radius: 4px;
    padding: 10px;
    font-family: "Consolas", "Menlo", monospace;
    font-size: 11px;
    line-height: 1.5;
}}

QScrollBar:vertical {{
    background: {BG_2};
    width: 10px;
    border: none;
}}

QScrollBar::handle:vertical {{
    background: {BORDER_HI};
    border-radius: 5px;
    min-height: 30px;
}}

QScrollBar::handle:vertical:hover {{
    background: {MUTED};
}}

QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {{
    height: 0;
}}

QGroupBox {{
    border: 1px solid {BORDER};
    border-radius: 8px;
    margin-top: 14px;
    padding-top: 16px;
    font-size: 11px;
    color: {MUTED};
    font-weight: 500;
}}

QGroupBox::title {{
    left: 14px;
    padding: 0 8px;
    subcontrol-origin: margin;
    color: {MUTED};
    background: {BG};
}}

QDialog QLabel {{
    color: {TEXT};
}}

QTabWidget::pane {{
    border: none;
}}

QTabBar::tab {{
    background: transparent;
    color: {MUTED};
    padding: 8px 18px;
    border: none;
    border-bottom: 2px solid transparent;
}}

QTabBar::tab:selected {{
    color: {ACCENT};
    border-bottom-color: {ACCENT};
}}

QTabBar::tab:hover:!selected {{
    color: {TEXT};
}}
"""
