//! Every `devbox` command exists; those without behaviour yet say so.

use std::process::ExitCode;

fn devbox(args: &[&str]) -> (ExitCode, String, String) {
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code =
        devbox_cli::run(std::iter::once("devbox").chain(args.iter().copied()), &mut out, &mut err);
    (code, String::from_utf8(out).unwrap(), String::from_utf8(err).unwrap())
}

#[test]
fn commands_without_behaviour_say_so() {
    let commands: [&[&str]; 7] = [
        &["up"],
        &["down"],
        &["status"],
        &["doctor"],
        &["connect"],
        &["password", "wake"],
        &["password", "app"],
    ];
    for args in commands {
        let (code, out, err) = devbox(args);
        assert_eq!(code, ExitCode::from(3), "{args:?}");
        assert_eq!(out, "", "{args:?}");
        assert_eq!(err, format!("devbox {}: not implemented yet\n", args.join(" ")), "{args:?}");
    }
}

#[test]
fn help_lists_every_command_on_stdout() {
    let (code, out, err) = devbox(&["--help"]);
    assert_eq!(code, ExitCode::SUCCESS);
    assert_eq!(err, "");
    for command in ["up", "down", "status", "doctor", "connect", "password", "sync", "context"] {
        assert!(
            out.lines().any(|l| l.trim_start().starts_with(command)),
            "{command} missing:\n{out}"
        );
    }
}

#[test]
fn version_prints_on_stdout() {
    let (code, out, err) = devbox(&["--version"]);
    assert_eq!(code, ExitCode::SUCCESS);
    assert_eq!(out, format!("devbox {}\n", env!("CARGO_PKG_VERSION")));
    assert_eq!(err, "");
}

#[test]
fn unknown_commands_are_usage_errors_on_stderr() {
    for args in [&["bogus"][..], &[], &["password", "root"]] {
        let (code, out, err) = devbox(args);
        assert_eq!(code, ExitCode::from(2), "{args:?}");
        assert_eq!(out, "", "{args:?}");
        assert!(err.starts_with("error:") || err.contains("Usage: devbox"), "{args:?}: {err}");
    }
}
