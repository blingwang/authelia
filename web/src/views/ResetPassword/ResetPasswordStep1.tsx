import React, { useState } from "react";
import LoginLayout from "../../layouts/LoginLayout";
import { Grid, Button, makeStyles } from "@material-ui/core";
import { useNotifications } from "../../hooks/NotificationsContext";
import { useHistory } from "react-router";
import { initiateResetPasswordProcess } from "../../services/ResetPassword";
import { FirstFactorRoute } from "../../Routes";
import FixedTextField from "../../components/FixedTextField";

export default function () {
    const style = useStyles();
    const [username, setUsername] = useState("");
    const [error, setError] = useState(false);
    const { createInfoNotification, createErrorNotification } = useNotifications();
    const history = useHistory();

    const doInitiateResetPasswordProcess = async () => {
        if (username === "") {
            setError(true);
            return;
        }

        try {
            await initiateResetPasswordProcess(username);
            createInfoNotification("An email has been sent to your address to complete the process.");
        } catch (err) {
            createErrorNotification("There was an issue initiating the password reset process.");
        }
    }

    const handleResetClick = () => {
        doInitiateResetPasswordProcess();
    }

    const handleCancelClick = () => {
        history.push(FirstFactorRoute);
    }

    return (
        <LoginLayout title="Reset password" id="reset-password-step1-stage">
            <Grid container className={style.root} spacing={2}>
                <Grid item xs={12}>
                    <FixedTextField
                        id="username-textfield"
                        label="Username"
                        variant="outlined"
                        fullWidth
                        error={error}
                        value={username}
                        onChange={(e) => setUsername(e.target.value)}
                        onKeyPress={(ev) => {
                            if (ev.key === 'Enter') {
                                doInitiateResetPasswordProcess();
                                ev.preventDefault();
                            }
                        }} />
                </Grid>
                <Grid item xs={6}>
                    <Button
                        id="reset-button"
                        variant="contained"
                        color="primary"
                        fullWidth
                        onClick={handleResetClick}>Reset</Button>
                </Grid>
                <Grid item xs={6}>
                    <Button
                        id="cancel-button"
                        variant="contained"
                        color="primary"
                        fullWidth
                        onClick={handleCancelClick}>Cancel</Button>
                </Grid>
            </Grid>
        </LoginLayout>
    )
}

const useStyles = makeStyles(theme => ({
    root: {
        marginTop: theme.spacing(2),
        marginBottom: theme.spacing(2),
    },
}))