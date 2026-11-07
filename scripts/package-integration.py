"""Create a credential-free Home Assistant custom integration ZIP."""
from pathlib import Path
import sys
import zipfile


def package(destination):
    root = Path(__file__).resolve().parents[1]
    source = root / "custom_components" / "zaptec_exporter"
    destination = Path(destination)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(destination, "w", zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(source.rglob("*")):
            if path.is_file() and path.suffix in (".py", ".json") and "__pycache__" not in path.parts:
                archive.write(path, path.relative_to(root))
    return destination


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("Usage: python3 scripts/package-integration.py DESTINATION.zip")
    print(f"Packaged integration in {package(sys.argv[1])}")
