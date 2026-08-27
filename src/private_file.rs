use std::{
    fs::{self, File, OpenOptions},
    io::{Read, Write},
    path::{Path, PathBuf},
};

use serde::{Serialize, de::DeserializeOwned};

use crate::error::GatewayError;

const MAX_JSON_BYTES: u64 = 16 * 1024 * 1024;

pub fn read_json<T: DeserializeOwned>(path: &Path) -> Result<Option<T>, GatewayError> {
    let mut file = match File::open(path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(storage(path, error)),
    };
    let metadata = file.metadata().map_err(|error| storage(path, error))?;
    if !metadata.is_file() || metadata.len() > MAX_JSON_BYTES {
        return Err(GatewayError::Storage(format!(
            "unsafe private JSON file: {}",
            path.display()
        )));
    }
    let mut bytes = Vec::with_capacity(metadata.len() as usize);
    file.read_to_end(&mut bytes)
        .map_err(|error| storage(path, error))?;
    let value = serde_json::from_slice(&bytes).map_err(|error| {
        GatewayError::Storage(format!("invalid private JSON {}: {error}", path.display()))
    })?;
    Ok(Some(value))
}

pub fn write_json<T: Serialize>(path: &Path, value: &T) -> Result<(), GatewayError> {
    let parent = path.parent().ok_or_else(|| {
        GatewayError::Storage(format!("private path has no parent: {}", path.display()))
    })?;
    create_private_dir(parent)?;
    let bytes = serde_json::to_vec_pretty(value).map_err(|error| {
        GatewayError::Storage(format!("encode private JSON {}: {error}", path.display()))
    })?;
    if bytes.len() as u64 > MAX_JSON_BYTES {
        return Err(GatewayError::Storage(format!(
            "private JSON exceeds size limit: {}",
            path.display()
        )));
    }

    let temporary = temporary_path(path);
    let mut options = OpenOptions::new();
    options.create_new(true).write(true);
    set_private_file_mode(&mut options);
    let mut file = options
        .open(&temporary)
        .map_err(|error| storage(&temporary, error))?;
    let result = (|| {
        file.write_all(&bytes)
            .map_err(|error| storage(&temporary, error))?;
        file.write_all(b"\n")
            .map_err(|error| storage(&temporary, error))?;
        file.sync_all()
            .map_err(|error| storage(&temporary, error))?;
        fs::rename(&temporary, path).map_err(|error| storage(path, error))?;
        sync_directory(parent)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

pub fn write_text(path: &Path, value: &str) -> Result<(), GatewayError> {
    let parent = path.parent().ok_or_else(|| {
        GatewayError::Storage(format!("private path has no parent: {}", path.display()))
    })?;
    create_private_dir(parent)?;
    let temporary = temporary_path(path);
    let mut options = OpenOptions::new();
    options.create_new(true).write(true);
    set_private_file_mode(&mut options);
    let mut file = options
        .open(&temporary)
        .map_err(|error| storage(&temporary, error))?;
    let result = (|| {
        file.write_all(value.as_bytes())
            .map_err(|error| storage(&temporary, error))?;
        file.sync_all()
            .map_err(|error| storage(&temporary, error))?;
        fs::rename(&temporary, path).map_err(|error| storage(path, error))?;
        sync_directory(parent)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

pub fn append_line(path: &Path, value: &str) -> Result<(), GatewayError> {
    if value.contains(['\n', '\r']) {
        return Err(GatewayError::Storage(format!(
            "private log line contains a line break: {}",
            path.display()
        )));
    }
    prepare_private_file(path)?;
    let parent = path.parent().expect("private path was validated");
    let created = !path.exists();
    let mut options = OpenOptions::new();
    options.create(true).append(true);
    set_private_file_mode(&mut options);
    let mut file = options.open(path).map_err(|error| storage(path, error))?;
    let original_len = file.metadata().map_err(|error| storage(path, error))?.len();
    let mut line = Vec::with_capacity(value.len() + 1);
    line.extend_from_slice(value.as_bytes());
    line.push(b'\n');
    if let Err(error) = file.write_all(&line).and_then(|()| file.sync_data()) {
        file.set_len(original_len)
            .and_then(|()| file.sync_data())
            .map_err(|rollback| {
                GatewayError::Storage(format!(
                    "private log append and rollback failed {}: append={error}; rollback={rollback}",
                    path.display()
                ))
            })?;
        return Err(storage(path, error));
    }
    if created {
        sync_directory(parent)?;
    }
    Ok(())
}

pub fn prepare_private_file(path: &Path) -> Result<(), GatewayError> {
    let parent = path.parent().ok_or_else(|| {
        GatewayError::Storage(format!("private path has no parent: {}", path.display()))
    })?;
    create_private_dir(parent)?;
    let metadata = match fs::symlink_metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(storage(path, error)),
    };
    if metadata.file_type().is_symlink() || !metadata.file_type().is_file() {
        return Err(GatewayError::Storage(format!(
            "unsafe private file: {}",
            path.display()
        )));
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o600))
            .map_err(|error| storage(path, error))?;
    }
    Ok(())
}

pub fn create_marker(path: &Path, value: &str) -> Result<bool, GatewayError> {
    let parent = path.parent().ok_or_else(|| {
        GatewayError::Storage(format!("private path has no parent: {}", path.display()))
    })?;
    create_private_dir(parent)?;
    let mut options = OpenOptions::new();
    options.create_new(true).write(true);
    set_private_file_mode(&mut options);
    let mut file = match options.open(path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => return Ok(false),
        Err(error) => return Err(storage(path, error)),
    };
    if let Err(error) = file
        .write_all(value.as_bytes())
        .and_then(|()| file.sync_all())
        .and_then(|()| sync_directory(parent).map_err(std::io::Error::other))
    {
        let _ = fs::remove_file(path);
        return Err(storage(path, error));
    }
    Ok(true)
}

fn create_private_dir(path: &Path) -> Result<(), GatewayError> {
    fs::create_dir_all(path).map_err(|error| storage(path, error))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(0o700))
            .map_err(|error| storage(path, error))?;
    }
    Ok(())
}

fn temporary_path(path: &Path) -> PathBuf {
    let random: u64 = rand::random();
    let name = path
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or("private.json");
    path.with_file_name(format!(".{name}.{random:016x}.tmp"))
}

#[cfg(unix)]
fn set_private_file_mode(options: &mut OpenOptions) {
    use std::os::unix::fs::OpenOptionsExt;
    options.mode(0o600);
}

#[cfg(not(unix))]
fn set_private_file_mode(_: &mut OpenOptions) {}

fn sync_directory(path: &Path) -> Result<(), GatewayError> {
    #[cfg(unix)]
    File::open(path)
        .and_then(|directory| directory.sync_all())
        .map_err(|error| storage(path, error))?;
    Ok(())
}

fn storage(path: &Path, error: std::io::Error) -> GatewayError {
    GatewayError::Storage(format!("{}: {error}", path.display()))
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use super::*;

    #[test]
    fn test_private_json_round_trip_uses_atomic_file() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("state").join("settings.json");
        let expected = BTreeMap::from([("chatMode".to_owned(), "private".to_owned())]);
        write_json(&path, &expected).unwrap();
        let actual: BTreeMap<String, String> = read_json(&path).unwrap().unwrap();
        assert_eq!(actual, expected);
        assert_eq!(fs::read_dir(path.parent().unwrap()).unwrap().count(), 1);

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(path).unwrap().permissions().mode() & 0o777,
                0o600
            );
        }
    }

    #[test]
    fn test_private_log_append_is_line_delimited_and_private() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("telemetry").join("debug.jsonl");
        append_line(&path, "{\"event\":1}").unwrap();
        append_line(&path, "{\"event\":2}").unwrap();
        assert_eq!(
            fs::read_to_string(&path).unwrap(),
            "{\"event\":1}\n{\"event\":2}\n"
        );
        assert!(append_line(&path, "forbidden\nsecond line").is_err());

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(path).unwrap().permissions().mode() & 0o777,
                0o600
            );
        }
    }

    #[cfg(unix)]
    #[test]
    fn test_private_log_repairs_existing_mode_and_rejects_symlink() {
        use std::os::unix::fs::{PermissionsExt, symlink};

        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("debug.jsonl");
        fs::write(&path, "").unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap();
        append_line(&path, "{\"event\":1}").unwrap();
        assert_eq!(
            fs::metadata(&path).unwrap().permissions().mode() & 0o777,
            0o600
        );

        let target = root.path().join("target.jsonl");
        let linked = root.path().join("linked.jsonl");
        fs::write(&target, "unchanged\n").unwrap();
        symlink(&target, &linked).unwrap();
        assert!(append_line(&linked, "forbidden").is_err());
        assert_eq!(fs::read_to_string(target).unwrap(), "unchanged\n");
    }
}
